package simulator

import (
	"encoding/hex"
	"net/url"
	"time"
)

func (s *Server) routeDBBackup(method, path string, body object) (int, any, bool, bool) {
	u, err := url.Parse(path)
	if err != nil {
		return 0, nil, false, false
	}
	respond := func(status int, payload any) (int, any, bool, bool) { return status, payload, false, true }
	const instance = "app.kubernetes.io/instance"
	switch method + " " + u.Path {
	case "GET /api/getEnv":
		a, b := good(object{"BACKUP_ENABLED": true})
		return respond(a, b)
	case "GET /api/getDBByName":
		name := u.Query().Get("name")
		db, exists := s.dbs[name]
		if !exists {
			a, b := bad(404, "Database not found.")
			return respond(a, b)
		}
		a, b := good(object{"dbName": name, "dbType": db["dbType"], "dbVersion": db["dbVersion"]})
		return respond(a, b)
	case "GET /api/backup/getBackupList":
		db := u.Query().Get("dbName")
		if db == "" {
			a, b := bad(400, "Backup list requires the database name.")
			return respond(a, b)
		}
		items := []any{}
		for _, backup := range s.backups {
			if lookup(backup, "metadata", "labels", instance) == db {
				items = append(items, backup)
			}
		}
		a, b := good(items)
		return respond(a, b)
	case "POST /api/backup/create":
		name, dbName, engine := text(body["backupName"]), text(body["dbName"]), text(body["dbType"])
		db, exists := s.dbs[dbName]
		method := map[string]string{"postgresql": "pg-basebackup", "mongodb": "dump", "apecloud-mysql": "xtrabackup", "redis": "datafile"}[engine]
		if !exists || engine != db["dbType"] || method == "" || name == "" {
			a, b := bad(400, "Invalid database or backup target.")
			return respond(a, b)
		}
		if _, exists := s.backups[name]; exists || s.Fault == "conflict" {
			a, b := bad(409, "Backup already exists or the write conflicted.")
			return respond(a, b)
		}
		remark := text(body["remark"])
		if len(remark) > 30 {
			a, b := bad(400, "Backup remark exceeds the limit.")
			return respond(a, b)
		}
		policy := engine
		if engine == "apecloud-mysql" {
			policy = "mysql"
		}
		if s.backups == nil {
			s.backups = map[string]object{}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		s.backups[name] = object{
			"metadata": object{
				"name": name, "namespace": "ns-demo", "uid": "mock-" + name, "creationTimestamp": now,
				"labels":      object{instance: dbName, "backup-remark": hex.EncodeToString([]byte(remark))},
				"annotations": object{"dataprotection.kubeblocks.io/connection-password": "SIMULATED_BACKUP_PRIVATE_VALUE"},
			},
			"spec":   object{"backupMethod": method, "backupPolicyName": dbName + "-" + policy + "-backup-policy"},
			"status": object{"phase": "Completed", "completionTimestamp": now, "totalSize": "1024"},
		}
		a, b := good(nil)
		return a, b, true, true
	default:
		return 0, nil, false, false
	}
}
