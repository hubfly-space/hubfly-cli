package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func uploadLocalImage(localTag string, session deploySessionResponse) (string, error) {
	return uploadLocalImageContext(context.Background(), localTag, session)
}

// uploadLocalImageContext pushes the local image to the session's regional
// registry, retrying transient failures and verifying the pushed digest.
func uploadLocalImageContext(ctx context.Context, localTag string, session deploySessionResponse) (string, error) {
	if session.Upload.Mode != "direct_registry" {
		return "", fmt.Errorf("server returned unsupported upload mode %q", session.Upload.Mode)
	}
	if strings.TrimSpace(session.Upload.PushRef) == "" {
		return "", fmt.Errorf("server did not return a registry push reference")
	}
	if strings.TrimSpace(session.Upload.CanonicalRef) == "" {
		return "", fmt.Errorf("server did not return a canonical image reference")
	}
	if strings.TrimSpace(session.Upload.Token) == "" {
		return "", fmt.Errorf("server did not return an upload token")
	}

	printDeployStep("Registry push", session.Upload.CanonicalRef)

	sourceRef, err := name.NewTag(localTag, name.WeakValidation)
	if err != nil {
		return "", fmt.Errorf("invalid local image tag %q: %w", localTag, err)
	}
	img, err := daemon.Image(sourceRef)
	if err != nil {
		return "", fmt.Errorf("read local docker image %q: %w", localTag, err)
	}
	digest, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("calculate local image digest: %w", err)
	}

	tagOptions := []name.Option{name.WeakValidation}
	if strings.EqualFold(session.Upload.RegistryScheme, "http") {
		tagOptions = append(tagOptions, name.Insecure)
	}
	targetRef, err := name.NewTag(session.Upload.PushRef, tagOptions...)
	if err != nil {
		return "", fmt.Errorf("invalid registry push reference %q: %w", session.Upload.PushRef, err)
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Minute)
	defer cancel()

	progress := newUploadProgress("Upload progress", 0)
	progress.Start()
	defer progress.Finish()

	baseOptions := []remote.Option{
		remote.WithAuth(&authn.Bearer{Token: session.Upload.Token}),
		// ggcr retries individual registry requests briefly; keep that
		// fast and let the loop below handle longer outages.
		remote.WithRetryBackoff(remote.Backoff{Duration: time.Second, Factor: 2, Jitter: 0.1, Steps: 3}),
	}
	if strings.EqualFold(session.Upload.RegistryScheme, "http") {
		baseOptions = append(baseOptions, remote.WithTransport(http.DefaultTransport))
	}

	// Each attempt re-checks which layer blobs the registry already holds
	// (HEAD per blob) and only sends the missing ones, so a retry after a
	// dropped connection resumes at layer granularity instead of starting
	// over. The registry verifies every blob against its sha256 digest.
	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		updates := make(chan v1.Update, 32)
		forwarded := make(chan struct{})
		go func() {
			defer close(forwarded)
			for update := range updates {
				if update.Total > 0 {
					progress.SetTotal(update.Total)
				}
				if update.Complete >= 0 {
					progress.SetCurrent(update.Complete)
				}
			}
		}()
		options := append(append([]remote.Option{}, baseOptions...), remote.WithContext(ctx), remote.WithProgress(updates))
		lastErr = remote.Write(targetRef, img, options...)
		<-forwarded
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil || !isTransientDeployError(lastErr) || attempt == maxAttempts {
			break
		}
		delay := deployBackoff(attempt, 30*time.Second)
		progress.Note(fmt.Sprintf("Upload interrupted (%v); resuming in %s (attempt %d/%d)", lastErr, delay.Round(time.Second), attempt+1, maxAttempts))
		if !sleepContext(ctx, delay) {
			lastErr = ctx.Err()
			break
		}
	}
	if lastErr != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("push image to regional registry: upload took longer than 60 minutes: %w", lastErr)
		}
		return "", fmt.Errorf("push image to regional registry: %w", lastErr)
	}

	// Integrity check: the manifest the registry now serves under the push
	// reference must be the exact image we built.
	headOptions := append(append([]remote.Option{}, baseOptions...), remote.WithContext(ctx))
	if descriptor, headErr := remote.Head(targetRef, headOptions...); headErr == nil {
		if descriptor.Digest != digest {
			return "", fmt.Errorf("uploaded image digest mismatch: registry has %s, local image is %s", descriptor.Digest, digest)
		}
	} else {
		debugf("registry digest verification skipped: %v", headErr)
	}

	return digest.String(), nil
}
