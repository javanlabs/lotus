package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func readDeploy(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../deploy/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// serve exits when it cannot create LOTUS_BLOB_DIR. The unit runs with
// ProtectSystem=strict, so the blob directory must be a path systemd makes
// writable for it, and the rendered env must point there.
func TestDeploy_BlobDirIsWritableUnderTheUnitsSandbox(t *testing.T) {
	unit := readDeploy(t, "lotuscheck-web.service")
	if !strings.Contains(unit, "ProtectSystem=strict") {
		t.Skip("the unit no longer sandboxes the filesystem")
	}
	if !regexp.MustCompile(`(?m)^StateDirectory=lotuscheck$`).MatchString(unit) {
		t.Fatal("lotuscheck-web.service has no StateDirectory=lotuscheck: /var/lib/lotuscheck is read-only to serve")
	}
	env := readDeploy(t, "deploy.sh")
	if !regexp.MustCompile(`(?m)^LOTUS_BLOB_DIR=\$\{LOTUS_BLOB_DIR:-/var/lib/lotuscheck/blob\}$`).MatchString(env) {
		t.Fatal("deploy.sh does not render LOTUS_BLOB_DIR under /var/lib/lotuscheck")
	}
}

// The app accepts 25 MB bodies (attachments), and reads a body whole before
// any middleware runs. nginx therefore keeps the site at 4 MB and lets
// 25 MB through only on the upload routes.
func TestDeploy_NginxAllowsLargeBodiesOnlyOnUploads(t *testing.T) {
	conf := readDeploy(t, "nginx-lotuscheck.conf")
	sizes := regexp.MustCompile(`client_max_body_size\s+(\S+);`).FindAllStringSubmatch(conf, -1)
	if len(sizes) != 2 || sizes[0][1] != "4m" || sizes[1][1] != "25m" {
		t.Fatalf("client_max_body_size = %v; want 4m for the site and 25m in one upload location", sizes)
	}
	upload := conf[strings.Index(conf, "client_max_body_size 25m"):]
	upload = conf[strings.LastIndex(conf[:len(conf)-len(upload)], "location"):]
	if !strings.Contains(upload[:strings.Index(upload, "{")], "/qa/") {
		t.Fatalf("the 25m block is not the QA upload location:\n%s", upload[:200])
	}
}
