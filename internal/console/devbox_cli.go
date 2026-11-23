package console

import "time"

func devboxCommandShape(pos []string) error {
	n := 0
	switch pos[1] {
	case "connect", "list":
		n = 2
	case "create", "get", "wait", "exec", "start", "stop", "ports":
		n = 3
	case "public":
		if len(pos) >= 3 && (pos[2] == "on" || pos[2] == "off") {
			n = 4
		}
	case "template", "release":
		if len(pos) >= 3 {
			switch pos[1] + " " + pos[2] {
			case "template list":
				n = 3
			case "template create", "template versions", "release list", "release create", "release wait":
				n = 4
			}
		}
	}
	if n == 0 || len(pos) != n {
		return fail("INPUT", "Unknown DevBox command or incorrect positional arguments; use --help.")
	}
	return nil
}

func devboxWrites(pos []string) bool {
	switch pos[1] {
	case "connect", "create", "start", "stop", "exec", "public":
		return true
	case "template", "release":
		return pos[2] == "create"
	default:
		return false
	}
}

func runDevbox(s *Session, original Profile, profiles Profiles, o options, pos []string, wait time.Duration) (any, error) {
	if o.dryRun && o.execute {
		return nil, fail("INPUT", "Use either --dry-run or --execute.")
	}
	timeout := time.Duration(o.timeout) * time.Second
	switch pos[1] {
	case "connect":
		plan, err := s.connectDevbox(o.execute)
		if err == nil && o.execute {
			original.Devbox = s.Profile.Devbox
			err = profiles.Save(o.profile, original)
		}
		return plan, err
	case "list":
		rows, err := s.devboxList()
		if err != nil {
			return nil, err
		}
		items := []Object{}
		for _, row := range rows {
			items = append(items, devboxSummary(row))
		}
		return Object{"namespace": s.Namespace, "items": items}, nil
	case "get":
		box, err := s.devboxGet(pos[2])
		if err != nil {
			return nil, err
		}
		out := devboxSummary(box)
		out["namespace"] = s.Namespace
		return out, nil
	case "ports":
		return s.devboxPorts(pos[2])
	case "public":
		return s.setDevboxPublic(pos[3], o.port, pos[2] == "on", o.execute)
	case "wait":
		return s.devboxWait(pos[2], "", []string{"Running"}, timeout)
	case "create":
		return s.createDevbox(devboxCreateOptions{name: pos[2], repo: o.template, version: o.templateVersion,
			cpu: o.cpuMilli, memory: o.memoryMi, storage: o.storageGi, port: o.port,
			public: o.public, execute: o.execute, wait: wait})
	case "start":
		return s.devboxPower(pos[2], "start", o.execute, wait)
	case "stop":
		mode := map[string]string{"retain-port": "Stopped", "release-port": "Shutdown"}[o.mode]
		if mode == "" {
			return nil, fail("INPUT", "Specify --mode retain-port or release-port.")
		}
		return s.devboxPower(pos[2], mode, o.execute, wait)
	case "exec":
		return s.devboxExec(pos[2], o.command, o.execute, o.json, timeout)
	case "template":
		switch pos[2] {
		case "list":
			scope := o.scope
			if scope == "global" {
				scope = "official"
			}
			return s.devboxTemplateList(scope, o.search, o.page)
		case "versions":
			rows, err := s.devboxTemplateVersions(pos[3])
			if err != nil {
				return nil, err
			}
			items := []Object{}
			for _, row := range rows {
				items = append(items, selectFields(row, "uid", "name", "image", "createdAt", "updatedAt"))
			}
			return Object{"repositoryUID": pos[3], "items": items}, nil
		case "create":
			return s.templateFromRelease(o.from, o.tag, pos[3], o.templateVersion, o.notes, o.execute)
		}
	case "release":
		if pos[2] == "create" {
			return s.releaseDevbox(pos[3], o.tag, o.notes, o.execute, wait)
		}
		box, err := s.devboxGet(pos[3])
		if err != nil {
			return nil, err
		}
		uid := devboxID(box)
		if pos[2] == "wait" {
			if !devboxTag.MatchString(o.tag) {
				return nil, fail("INPUT", "Specify a release --tag.")
			}
			return s.waitDevboxRelease(pos[3], uid, o.tag, timeout)
		}
		rows, err := s.devboxReleases(pos[3], uid)
		if err != nil {
			return nil, err
		}
		items := []Object{}
		for _, row := range rows {
			items = append(items, devboxReleaseSummary(row))
		}
		return Object{"namespace": s.Namespace, "name": pos[3], "uid": uid, "items": items}, nil
	}
	return nil, fail("INPUT", "Unknown DevBox command.")
}
