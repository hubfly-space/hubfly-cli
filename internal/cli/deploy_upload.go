package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func uploadLocalImage(localTag string, session deploySessionResponse) error {
	if session.Upload.Mode != "direct_registry" {
		return fmt.Errorf("server returned unsupported upload mode %q", session.Upload.Mode)
	}
	if strings.TrimSpace(session.Upload.PushRef) == "" {
		return fmt.Errorf("server did not return a registry push reference")
	}
	if strings.TrimSpace(session.Upload.CanonicalRef) == "" {
		return fmt.Errorf("server did not return a canonical image reference")
	}
	if strings.TrimSpace(session.Upload.Token) == "" {
		return fmt.Errorf("server did not return an upload token")
	}

	printDeployStep("Registry push", session.Upload.CanonicalRef)

	sourceRef, err := name.NewTag(localTag, name.WeakValidation)
	if err != nil {
		return fmt.Errorf("invalid local image tag %q: %w", localTag, err)
	}
	img, err := daemon.Image(sourceRef)
	if err != nil {
		return fmt.Errorf("read local docker image %q: %w", localTag, err)
	}

	tagOptions := []name.Option{name.WeakValidation}
	if strings.EqualFold(session.Upload.RegistryScheme, "http") {
		tagOptions = append(tagOptions, name.Insecure)
	}
	targetRef, err := name.NewTag(session.Upload.PushRef, tagOptions...)
	if err != nil {
		return fmt.Errorf("invalid registry push reference %q: %w", session.Upload.PushRef, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	writeOptions := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuth(&authn.Bearer{Token: session.Upload.Token}),
	}
	if strings.EqualFold(session.Upload.RegistryScheme, "http") {
		writeOptions = append(writeOptions, remote.WithTransport(http.DefaultTransport))
	}
	if err := remote.Write(targetRef, img, writeOptions...); err != nil {
		return fmt.Errorf("push image to regional registry: %w", err)
	}

	return nil
}
