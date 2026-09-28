package legacylotus

import "strings"

// trivyImageToComponent maps the legacy Harbor image stem (the repository
// name with its -dev / -release suffix removed) onto a component slug.
// The old naming scheme is consistent enough that this short table covers
// every AlurKerja image; anything not listed is reported and skipped
// rather than guessed at.
var trivyImageToComponent = map[string]string{
	"alurkerja-apps":          "app-react-v2",
	"alurkerja-auth":          "authentication-service",
	"alurkerja-bpm":           "bpm-service",
	"alurkerja-camunda":       "camunda",
	"alurkerja-compro-be":     "company-profile-service",
	"alurkerja-compro":        "company-profile-react",
	"alurkerja-generate-test": "generate-test-service",
	"alurkerja-integration":   "integration-service",
	"alurkerja-microsite":     "microsite",
	"alurkerja-migration":     "migration-service",
	"alurkerja-notification":  "notification-service",
	"alurkerja-proxy":         "proxy-service",
	"alurkerja-report":        "report-service",
	"alurkerja-simulation-be": "simulation-service",
	"alurkerja-simulation":    "simulation-react",
	"alurkerja-studio":        "studio-react",
	"alurkerja-tenant":        "tenant-management-service",
}

func imageStem(image string) string {
	for _, suffix := range []string{"-new-release", "-release", "-dev"} {
		if strings.HasSuffix(image, suffix) {
			return strings.TrimSuffix(image, suffix)
		}
	}
	return image
}

// TrivyComponentSlug is the component slug a legacy Harbor image belongs
// to (trivyImageToComponent), and whether it has one.
func TrivyComponentSlug(image string) (string, bool) {
	slug, ok := trivyImageToComponent[imageStem(image)]
	return slug, ok
}
