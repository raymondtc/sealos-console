package console

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dbBackupReadPath   = "/api/backup/getBackupList"
	dbBackupCreatePath = "/api/backup/create"
	dbBackupRemarkSize = 30
)

// The page gates creation on these four engines. The provider selects the
// method and policy from dbType, so the request does not accept overrides.
var dbBackupEngine = map[string]struct{ policy, method string }{
	"postgresql":     {"postgresql", "pg-basebackup"},
	"mongodb":        {"mongodb", "dump"},
	"apecloud-mysql": {"mysql", "xtrabackup"},
	"redis":          {"redis", "datafile"},
}

type backupDatabase struct {
	Name    string `json:"dbName"`
	Engine  string `json:"dbType"`
	Version string `json:"dbVersion"`
}

type dbBackupRow struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
		CreatedAt string `json:"creationTimestamp"`
		Labels    struct {
			Database string  `json:"app.kubernetes.io/instance"`
			Auto     *string `json:"dataprotection.kubeblocks.io/autobackup"`
			Remark   string  `json:"backup-remark"`
		} `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Method         string `json:"backupMethod"`
		Policy         string `json:"backupPolicyName"`
		Retention      string `json:"retentionPeriod"`
		DeletionPolicy string `json:"deletionPolicy"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		StartedAt  string `json:"startTimestamp"`
		FinishedAt string `json:"completionTimestamp"`
		ExpiresAt  string `json:"expiration"`
		Size       string `json:"totalSize"`
		Duration   string `json:"duration"`
	} `json:"status"`
}

// The scoped list includes raw backup objects. Decode only the fields needed
// here, leaving annotations, connection data and backend error bodies opaque.
func (s *Session) readDBBackupAPI(path string, output any) error {
	base, err := s.providerBase("dbprovider")
	if err != nil {
		return err
	}
	resp, err := s.HTTP.request(s.HTTP.Client, base, "GET", path, s.kubeconfig, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	route, _, _ := strings.Cut(path, "?")
	var envelope struct {
		Code int
		Data json.RawMessage
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&envelope); err != nil {
		return fail("PROTOCOL", "Invalid database provider response at %s (HTTP %d).", route, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code < 200 || envelope.Code >= 300 {
		return &Failure{Code: "API_ERROR", Status: resp.StatusCode, Message: fmt.Sprintf("GET %s failed: HTTP %d, business code %d.", route, resp.StatusCode, envelope.Code)}
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, output) != nil {
		return fail("PROTOCOL", "Missing or invalid database provider data at %s.", route)
	}
	return nil
}

func (s *Session) backupDatabase(name string) (backupDatabase, error) {
	var db backupDatabase
	if err := validName(name); err != nil {
		return db, err
	}
	if err := s.readDBBackupAPI("/api/getDBByName?name="+url.QueryEscape(name), &db); err != nil {
		return db, err
	}
	if db.Name != name || db.Engine == "" {
		return db, fail("PROTOCOL", "Database provider did not identify the requested database and engine.")
	}
	return db, nil
}

func (s *Session) dbBackupRows(db backupDatabase) ([]dbBackupRow, error) {
	var rows []dbBackupRow
	if err := s.readDBBackupAPI(dbBackupReadPath+"?dbName="+url.QueryEscape(db.Name), &rows); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, row := range rows {
		m := row.Metadata
		if m.Namespace != s.Namespace || m.Labels.Database != db.Name || !dnsName(m.Name) || names[m.Name] {
			return nil, fail("PROTOCOL", "Backup list contains an invalid, duplicate, or out-of-scope backup.")
		}
		names[m.Name] = true
	}
	return rows, nil
}

func (row dbBackupRow) phase() string {
	if row.Status.Phase == "" {
		return "Pending"
	}
	return row.Status.Phase
}

func (row dbBackupRow) summary(db backupDatabase) Object {
	m, spec, status := row.Metadata, row.Spec, row.Status
	kind := "manual"
	if m.Labels.Auto != nil {
		kind = "auto"
	}
	out := Object{"name": m.Name, "uid": m.UID, "namespace": m.Namespace, "dbName": db.Name, "dbType": db.Engine,
		"type": kind, "status": row.phase(), "createdAt": m.CreatedAt, "method": spec.Method, "policyName": spec.Policy}
	for name, value := range map[string]string{
		"startedAt": status.StartedAt, "completedAt": status.FinishedAt, "expiresAt": status.ExpiresAt,
		"retentionPeriod": spec.Retention, "deletionPolicy": spec.DeletionPolicy, "totalSize": status.Size, "duration": status.Duration,
	} {
		if value != "" {
			out[name] = value
		}
	}
	if label := m.Labels.Remark; label != "" && len(label) <= dbBackupRemarkSize*2 {
		if decoded, err := hex.DecodeString(label); err == nil && utf8.Valid(decoded) {
			out["remark"] = string(decoded)
		}
	}
	return out
}

func (s *Session) listDBBackups(db backupDatabase) (Object, error) {
	rows, err := s.dbBackupRows(db)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Metadata.CreatedAt > rows[j].Metadata.CreatedAt
	})
	items := make([]Object, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.summary(db))
	}
	return Object{"namespace": s.Namespace, "dbName": db.Name, "dbType": db.Engine, "items": items}, nil
}

func (s *Session) findDBBackup(db backupDatabase, name string) (*dbBackupRow, error) {
	rows, err := s.dbBackupRows(db)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Metadata.Name == name {
			return &row, nil
		}
	}
	return nil, nil
}

func (s *Session) waitDBBackup(db backupDatabase, name string, timeout time.Duration, expectedMethod, expectedPolicy string) (Object, error) {
	end := time.Now().Add(timeout)
	for {
		row, err := s.findDBBackup(db, name)
		if err != nil {
			return nil, err
		}
		if row != nil {
			if expectedMethod != "" && (row.Spec.Method != expectedMethod || row.Spec.Policy != expectedPolicy || row.Metadata.Labels.Auto != nil) {
				return nil, fail("WRITE_UNVERIFIED", "Visible backup does not match the submitted method, policy, or manual type.")
			}
			switch row.phase() {
			case "Completed":
				return row.summary(db), nil
			case "Failed":
				return nil, fail("BACKUP_FAILED", "Backup %q reached Failed. Use db backup get to inspect its metadata.", name)
			}
		}
		if !time.Now().Before(end) {
			return nil, fail("WAIT_TIMEOUT", "Backup %q did not reach Completed before timeout. Inspect it before attempting another creation.", name)
		}
		sleepUntil(end)
	}
}

func (s *Session) createDBBackup(db backupDatabase, name, notes string, execute bool, wait time.Duration) (Object, error) {
	rule, supported := dbBackupEngine[db.Engine]
	if !supported {
		return nil, fail("UNSUPPORTED", "This database engine does not support manual backup in the Console.")
	}
	if !utf8.ValidString(notes) || len(notes) > dbBackupRemarkSize || strings.ContainsRune(notes, 0) {
		return nil, fail("INPUT", "Backup notes must be valid UTF-8, at most 30 bytes, without NUL.")
	}
	var cfg struct {
		Enabled bool `json:"BACKUP_ENABLED"`
	}
	if err := s.readDBBackupAPI("/api/getEnv", &cfg); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, fail("UNSUPPORTED", "The Console database backup feature is disabled.")
	}
	existing, err := s.findDBBackup(db, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fail("ALREADY_EXISTS", "Backup %q already exists for this database. Inspect it before creating another.", name)
	}
	policy := db.Name + "-" + rule.policy + "-backup-policy"
	plan := Object{"operation": "db.backup.create", "path": dbBackupCreatePath, "method": "POST",
		"namespace": s.Namespace, "dbName": db.Name, "dbType": db.Engine, "dbVersion": db.Version, "name": name,
		"backupMethod": rule.method, "policyName": policy, "executed": false}
	if !execute {
		return plan, nil
	}
	body := Object{"dbName": db.Name, "dbType": db.Engine, "backupName": name}
	if notes != "" {
		body["remark"] = notes
	}
	if _, err := s.Provider("dbprovider", "POST", dbBackupCreatePath, body, true); err != nil {
		return nil, err
	}
	plan["executed"], plan["state"] = true, "submitted"
	if wait > 0 {
		backup, err := s.waitDBBackup(db, name, wait, rule.method, policy)
		if err != nil {
			return nil, err
		}
		plan["state"], plan["backup"] = "completed", backup
	}
	return plan, nil
}

func dbBackupCommandShape(pos []string) error {
	if len(pos) == 4 && pos[2] == "list" {
		return nil
	}
	if len(pos) == 5 {
		switch pos[2] {
		case "get", "create", "wait":
			return nil
		}
	}
	return fail("INPUT", "Use db backup list DB, or db backup get|create|wait DB BACKUP.")
}

func runDBBackup(s *Session, o options, pos []string, wait time.Duration) (Object, error) {
	action := pos[2]
	if o.execute && (o.dryRun || action != "create") {
		return nil, fail("INPUT", "--execute applies only to db backup create and cannot be combined with --dry-run.")
	}
	if action != "list" && !dnsName(pos[4]) {
		return nil, fail("INPUT", "Backup name must be a lowercase DNS name.")
	}
	db, err := s.backupDatabase(pos[3])
	if err != nil {
		return nil, err
	}
	switch action {
	case "list":
		return s.listDBBackups(db)
	case "create":
		return s.createDBBackup(db, pos[4], o.notes, o.execute, wait)
	case "wait":
		return s.waitDBBackup(db, pos[4], time.Duration(o.timeout)*time.Second, "", "")
	}
	row, err := s.findDBBackup(db, pos[4])
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fail("NOT_FOUND", "Backup %q was not found for the selected database.", pos[4])
	}
	return row.summary(db), nil
}
