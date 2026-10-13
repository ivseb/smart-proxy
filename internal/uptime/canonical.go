package uptime

import "strings"

// Canonical spells a workload reference one way: "web" for a Deployment ("deployment/web" too),
// "statefulset/db", "deploymentconfig/legacy".
func Canonical(ref string) string {
	kind, name, ok := strings.Cut(ref, "/")
	if !ok {
		return ref
	}
	switch strings.ToLower(kind) {
	case "deployment", "deployments", "deploy":
		return name
	case "statefulset", "statefulsets", "sts":
		return "statefulset/" + name
	case "deploymentconfig", "deploymentconfigs", "dc":
		return "deploymentconfig/" + name
	}
	return ref
}
