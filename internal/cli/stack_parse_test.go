package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStackSpecParsesComposeFile(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ".env"), []byte("TAG=1.25-alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := `name: sample
services:
  web:
    image: nginx:${TAG}
    ports:
      - "8080:80"
    depends_on:
      - db
  db:
    image: postgres:16
    volumes:
      - db-data:/var/lib/postgresql/data
volumes:
  db-data:
    x-hubfly:
      sizeGb: 25
`
	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := loadStackSpec(composePath)
	if err != nil {
		t.Fatalf("loadStackSpec returned error: %v", err)
	}
	if spec.Name != "sample" {
		t.Fatalf("expected stack name sample, got %s", spec.Name)
	}
	if len(spec.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(spec.Services))
	}
	if spec.Services[1].Image != "nginx:1.25-alpine" {
		t.Fatalf("expected interpolated image tag, got %s", spec.Services[1].Image)
	}
	if spec.Volumes["db-data"].SizeGb != 25 {
		t.Fatalf("expected volume size 25, got %d", spec.Volumes["db-data"].SizeGb)
	}
}

func TestLoadStackSpecWarnsOnBindMount(t *testing.T) {
	tmpDir := t.TempDir()
	compose := `services:
  app:
    image: busybox
    volumes:
      - ./data:/data
`
	composePath := filepath.Join(tmpDir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	spec, err := loadStackSpec(composePath)
	if err != nil {
		t.Fatalf("loadStackSpec returned error: %v", err)
	}
	if len(spec.Warnings) == 0 {
		t.Fatal("expected bind mount warning")
	}
	if len(spec.Services[0].Mounts) != 0 {
		t.Fatal("expected bind mounts to be ignored")
	}
}

func TestStackDeployOrderResolvesDependencies(t *testing.T) {
	ordered, err := stackDeployOrder([]stackServiceSpec{
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "db"},
		{Name: "worker", DependsOn: []string{"api"}},
	})
	if err != nil {
		t.Fatalf("stackDeployOrder returned error: %v", err)
	}
	if len(ordered) != 3 {
		t.Fatalf("expected 3 services, got %d", len(ordered))
	}
	if ordered[0].Name != "db" || ordered[1].Name != "api" || ordered[2].Name != "worker" {
		t.Fatalf("unexpected order: %s, %s, %s", ordered[0].Name, ordered[1].Name, ordered[2].Name)
	}
}
