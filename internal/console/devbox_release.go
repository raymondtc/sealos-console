package console

import (
	"net/url"
	"strings"
	"time"
)

func (s *Session) devboxReleases(name, uid string) ([]any, error) {
	q := url.Values{"devboxName": {name}, "devboxUid": {uid}}
	v, err := s.devboxAPI("GET", "/api/getDevboxVersionList?"+q.Encode(), "", nil)
	if err != nil {
		return nil, err
	}
	return devboxRows(v)
}

func findDevboxRelease(rows []any, tag string) (Object, error) {
	var found Object
	for _, row := range rows {
		if at(row, "tag") == tag {
			if found != nil {
				return nil, fail("CONFLICT", "More than one DevBox release has the requested tag.")
			}
			found = obj(row)
		}
	}
	return found, nil
}

func devboxReleaseSummary(v any) Object {
	result := selectFields(v, "id", "name", "devboxName", "tag", "description", "createTime", "image", "originalImage")
	result["status"] = devboxState(v)
	return result
}

func (s *Session) waitDevboxRelease(name, uid, tag string, wait time.Duration) (Object, error) {
	end := time.Now().Add(wait)
	for {
		rows, err := s.devboxReleases(name, uid)
		if err != nil {
			return nil, err
		}
		version, err := findDevboxRelease(rows, tag)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(devboxState(version)) {
		case "success", "succeeded":
			out := devboxReleaseSummary(version)
			if str(out["image"]) == "" {
				// Older Console builds omit the image from the release list.
				// Use the same reference convention as their Deploy action.
				env, err := s.devboxAPI("GET", "/api/getEnv", "", nil)
				if err == nil {
					registry := strings.TrimSuffix(str(at(env, "registryAddr")), "/")
					candidate := registry + "/" + s.Namespace + "/" + name + ":" + tag
					if registry != "" && validImage(candidate) == nil {
						out["image"], out["imageSource"] = candidate, "console registry convention"
					}
				}
			}
			return out, nil
		case "failed", "error":
			return nil, fail("DEVBOX_RELEASE_FAILED", "DevBox release %s failed; inspect its history before retrying.", tag)
		}
		if time.Now().After(end) {
			return nil, fail("WAIT_TIMEOUT", "Release %s last reported %q; inspect devbox release list before retrying.", tag, devboxState(version))
		}
		sleepUntil(end)
	}
}

func (s *Session) releaseDevbox(name, tag, notes string, execute bool, wait time.Duration) (Object, error) {
	if !devboxTag.MatchString(tag) {
		return nil, fail("INPUT", "Provide an explicit image tag with --tag.")
	}
	box, err := s.devboxGet(name)
	if err != nil {
		return nil, err
	}
	uid := devboxID(box)
	rows, err := s.devboxReleases(name, uid)
	if err != nil {
		return nil, err
	}
	prior, err := findDevboxRelease(rows, tag)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return nil, fail("ALREADY_EXISTS", "Release tag already exists; inspect it with devbox release list.")
	}
	state := strings.ToLower(devboxState(box))
	if state != "running" && state != "stopped" && state != "shutdown" {
		return nil, fail("INPUT", "Release requires a running or stopped DevBox.")
	}
	// The Console release dialog stops a running DevBox so its runtime
	// commits a snapshot. A release submitted during execution can stay
	// pending until that snapshot is available.
	stop := state == "running"
	plan := Object{"operation": "devbox release create", "name": name, "namespace": s.Namespace, "uid": uid,
		"tag": tag, "stopBeforeRelease": stop, "executed": false}
	if !execute {
		return plan, nil
	}
	if stop {
		if _, err := s.devboxAPI("POST", "/api/shutdownDevbox", "", Object{"devboxName": name, "shutdownMode": "Stopped"}); err != nil {
			return nil, err
		}
		if _, err := s.devboxWait(name, uid, []string{"Stopped"}, 120*time.Second); err != nil {
			return nil, err
		}
	}
	body := Object{"devboxName": name, "devboxUid": uid, "tag": tag, "releaseDes": notes}
	if _, err := s.devboxAPI("POST", "/api/releaseDevbox", "", body); err != nil {
		return nil, err
	}
	plan["executed"] = true
	if wait > 0 {
		version, err := s.waitDevboxRelease(name, uid, tag, wait)
		if err != nil {
			return nil, err
		}
		plan["release"] = version
	}
	return plan, nil
}

func (s *Session) templateFromRelease(boxName, tag, name, version, notes string, execute bool) (Object, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	if !devboxTag.MatchString(tag) || !devboxTag.MatchString(version) {
		return nil, fail("INPUT", "Specify a release --tag and a valid --template-version.")
	}
	if len([]rune(notes)) > 255 {
		return nil, fail("INPUT", "Template description must be at most 255 characters.")
	}
	box, err := s.devboxGet(boxName)
	if err != nil {
		return nil, err
	}
	uid := devboxID(box)
	rows, err := s.devboxReleases(boxName, uid)
	if err != nil {
		return nil, err
	}
	release, err := findDevboxRelease(rows, tag)
	if err != nil {
		return nil, err
	}
	state := strings.ToLower(devboxState(release))
	if (state != "success" && state != "succeeded") || str(release["name"]) == "" {
		return nil, fail("INPUT", "The source release must exist and have succeeded.")
	}
	plan := Object{"operation": "devbox template create", "namespace": s.Namespace, "name": name, "version": version,
		"sourceDevbox": boxName, "sourceTag": tag, "sourceRelease": release["name"],
		"visibility": "private", "executed": false}
	if !execute {
		return plan, nil
	}
	body := Object{"devboxReleaseName": release["name"], "templateRepositoryName": name,
		"version": version, "description": notes, "isPublic": false, "tagUidList": []string{}}
	if _, err := s.devboxTemplateAPI("POST", devboxTemplates+"/withTemplate/create", body); err != nil {
		return nil, err
	}
	plan["executed"] = true
	return plan, nil
}
