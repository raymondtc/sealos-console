package console

import (
	"regexp"
	"strings"
)

var appEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func appContainerUpdate(work, raw Object) (Object, Object, error) {
	groups := []string{"containers", "initContainers"}
	if err := checkFields(raw, []string{"containers", "initContainers", "originImageName"}, nil); err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 {
		return nil, nil, fail("INPUT", "Specify at least one container update or originImageName.")
	}
	if err := managedAppWorkload(work); err != nil {
		return nil, nil, err
	}
	pod := obj(at(work, "spec", "template", "spec"))
	podPatch, diff := Object{}, Object{}
	for _, group := range groups {
		input, exists := raw[group]
		if !exists {
			continue
		}
		edits, ok := input.([]any)
		if !ok || len(edits) == 0 {
			return nil, nil, fail("INPUT", "%s must be a nonempty array of container updates.", group)
		}
		updated, changes, err := updateContainerGroup(pod, group, edits)
		if err != nil {
			return nil, nil, err
		}
		if len(changes) != 0 {
			podPatch[group], diff[group] = updated, changes
		}
	}

	var originImage string
	if v, exists := raw["originImageName"]; exists {
		image := str(v)
		if err := validImage(image); err != nil {
			return nil, nil, err
		}
		current := str(at(work, "metadata", "annotations", "originImageName"))
		if work["kind"] != "Deployment" || current == "" {
			return nil, nil, fail("INPUT", "originImageName requires an existing top-level annotation on a Deployment.")
		}
		if current != image {
			originImage = image
			diff["originImageName"] = Object{"to": image}
		}
	}
	if len(diff) == 0 {
		return nil, diff, nil
	}
	body, err := appUpdateBody(work, podPatch, originImage)
	return body, diff, err
}

func updateContainerGroup(pod Object, group string, edits []any) ([]any, []Object, error) {
	// The console API replaces arrays. Copy every entry in its original order.
	updated := arr(clone(pod[group]))
	changes := []Object{}
	seen := map[string]bool{}
	for _, row := range edits {
		edit := obj(row)
		if err := checkFields(edit, []string{"name", "image", "env"}, []string{"name"}); err != nil {
			return nil, nil, err
		}
		name := str(edit["name"])
		if !dnsName(name) || strings.Contains(name, ".") || seen[name] {
			return nil, nil, fail("INPUT", "Container updates require unique valid names within %s.", group)
		}
		seen[name] = true
		if len(edit) == 1 {
			return nil, nil, fail("INPUT", "Specify image or env for %s %q.", group, name)
		}
		var container Object
		for _, v := range updated {
			if at(v, "name") == name {
				if container != nil {
					return nil, nil, fail("INPUT", "%s name %q is ambiguous.", group, name)
				}
				container = obj(v)
			}
		}
		if container == nil {
			return nil, nil, fail("INPUT", "%s name %q was not found.", group, name)
		}
		change := Object{"name": name}
		if value, exists := edit["image"]; exists {
			image := str(value)
			if err := validImage(image); err != nil {
				return nil, nil, err
			}
			old := str(container["image"])
			if len(arr(pod["imagePullSecrets"])) > 0 && registry(old) != registry(image) {
				return nil, nil, fail("UNSUPPORTED", "Changing registries with pull secrets needs a separate credential workflow.")
			}
			if old != image {
				container["image"] = image
				change["image"] = Object{"from": old, "to": image}
			}
		}
		if input, exists := edit["env"]; exists {
			names, err := updateContainerEnv(container, input)
			if err != nil {
				return nil, nil, err
			}
			if len(names) != 0 {
				change["environmentNames"] = names
			}
		}
		if len(change) > 1 {
			changes = append(changes, change)
		}
	}
	return updated, changes, nil
}

func updateContainerEnv(container Object, input any) ([]string, error) {
	edits, ok := input.([]any)
	if !ok || len(edits) == 0 {
		return nil, fail("INPUT", "env must be a nonempty array of name/value entries.")
	}
	env := arr(container["env"])
	if container["env"] != nil && env == nil {
		return nil, fail("PROTOCOL", "Container env is not an array.")
	}
	names := []string{}
	seen := map[string]bool{}
	for _, row := range edits {
		edit := obj(row)
		if err := checkFields(edit, []string{"name", "value"}, []string{"name", "value"}); err != nil {
			return nil, err
		}
		name := str(edit["name"])
		value, ok := edit["value"].(string)
		if !appEnvironmentName.MatchString(name) || seen[name] || !ok || strings.ContainsRune(value, 0) {
			return nil, fail("INPUT", "Use unique valid env names and string values without NUL.")
		}
		seen[name] = true
		var entry Object
		for _, v := range env {
			if at(v, "name") == name {
				if entry != nil || at(v, "valueFrom") != nil {
					return nil, fail("INPUT", "Env %q is ambiguous or uses valueFrom in container %q.", name, container["name"])
				}
				entry = obj(v)
			}
		}
		if entry == nil {
			entry = Object{"name": name}
			env = append(env, entry)
		} else if str(entry["value"]) == value {
			continue
		}
		if value == "" {
			delete(entry, "value") // The workload API omits empty EnvVar values.
		} else {
			entry["value"] = value
		}
		names = append(names, name)
	}
	if len(names) != 0 {
		container["env"] = env
	}
	return names, nil
}
