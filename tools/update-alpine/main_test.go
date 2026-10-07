package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleYAML = `-
  title: "Standard"
  flavor: alpine-standard
  branch: v3.24
  version: 3.24.2
  file: alpine-standard-3.24.2-x86_64.iso
  sha256: aaaa
-
  title: "Mini root filesystem"
  flavor: alpine-minirootfs
  branch: v3.24
  arch: x86_64
  version: 3.24.2
  file: alpine-minirootfs-3.24.2-x86_64.tar.gz
  sha256: c5ca053cfe1d85c5b96dff8b9bc57045f7f184a30ffb6b65776409ca90388677
-
  title: "Netboot"
  flavor: alpine-netboot
  branch: v3.24
  version: 3.24.2
  sha256: bbbb
`

func TestFetchMinirootfs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/empty" {
			w.Write([]byte("-\n  flavor: alpine-standard\n  version: 1\n"))
			return
		}
		w.Write([]byte(sampleYAML))
	}))
	defer srv.Close()

	r, err := fetchMinirootfs(srv.URL + "/latest-releases.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if r.Branch != "v3.24" || r.Version != "3.24.2" || r.SHA256 != "c5ca053cfe1d85c5b96dff8b9bc57045f7f184a30ffb6b65776409ca90388677" {
		t.Errorf("got %+v", r)
	}

	if _, err := fetchMinirootfs(srv.URL + "/empty"); err == nil {
		t.Error("expected error when no minirootfs entry")
	}
}
