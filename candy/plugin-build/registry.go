package build

// registry.go — the local OCI registry the buildkitd mirrors ghcr.io to.
//
// The mirror address is where every internal base image is pushed (the input half of the
// local-build bridge), so the DRIVE owns its lifecycle: depending on an operator-run
// container would make a correct build require out-of-band setup, which R4 forbids.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"
)

// registryContainerName is the deterministic name of the drive's local registry.
const registryContainerName = "charly-build-registry"

// ensureLocalRegistry makes the mirror's registry answer before a build needs it. A registry
// already answering /v2/ is reused; otherwise one is started on the mirror's own host:port.
func (c driveConfig) ensureLocalRegistry(ctx context.Context) error {
	if c.RegistryMirror == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(c.RegistryMirror)
	if err != nil {
		return fmt.Errorf("the build registry mirror %q is not host:port: %w", c.RegistryMirror, err)
	}
	if registryAnswers(c.RegistryMirror) {
		return nil
	}
	cmd := exec.CommandContext(ctx, "podman", "run", "-d",
		"--name", registryContainerName, "--replace",
		"-p", net.JoinHostPort(host, port)+":5000",
		"docker.io/library/registry:2")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting the local build registry on %s: %w: %s", c.RegistryMirror, err, out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if registryAnswers(c.RegistryMirror) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("the local build registry on %s did not answer /v2/ within 30s", c.RegistryMirror)
}

// registryAnswers reports whether something serves the registry API at addr.
func registryAnswers(addr string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/v2/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
}
