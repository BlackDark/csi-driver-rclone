package controller

import (
	"fmt"
	"strings"
)

// Parameter keys external-provisioner adds with --extra-create-metadata.
const (
	ParamPVCName      = "csi.storage.k8s.io/pvc/name"
	ParamPVCNamespace = "csi.storage.k8s.io/pvc/namespace"
	ParamPVName       = "csi.storage.k8s.io/pv/name"
)

// templateVars maps each remotePath variable to the parameter holding its value.
var templateVars = map[string]string{
	"pvc.namespace": ParamPVCNamespace,
	"pvc.name":      ParamPVCName,
	"pv.name":       ParamPVName,
}

// perVolumeVars are the variables that alone give every PVC its own path.
var perVolumeVars = []string{"pvc.name", "pv.name"}

// expandPath resolves ${var} references in tmpl from params and checks the result
// is a relative path without "." or ".." segments.
func expandPath(tmpl string, params map[string]string) (string, error) {
	var b strings.Builder
	perVolume := false
	rest := tmpl
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		end := strings.IndexByte(rest[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated ${ in remotePath")
		}
		name := rest[i+2 : i+end]
		key, ok := templateVars[name]
		if !ok {
			return "", fmt.Errorf("unknown remotePath variable ${%s}", name)
		}
		v := params[key]
		if v == "" {
			return "", fmt.Errorf("remotePath variable ${%s} has no value: run external-provisioner with --extra-create-metadata", name)
		}
		for _, p := range perVolumeVars {
			perVolume = perVolume || p == name
		}
		b.WriteString(v)
		rest = rest[i+end+1:]
	}
	if !perVolume {
		return "", fmt.Errorf("remotePath must contain ${pvc.name} or ${pv.name}, or every PVC shares one path")
	}
	return b.String(), checkPath(b.String())
}

func checkPath(p string) error {
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("remotePath must be relative")
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".", "..":
			return fmt.Errorf("remotePath has an empty, \".\" or \"..\" segment")
		}
	}
	return nil
}
