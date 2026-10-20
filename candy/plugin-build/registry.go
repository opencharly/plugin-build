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

	"github.com/opencharly/spec/container"
)

const (
	// registryContainerName is the deterministic name of the drive's local registry.
	registryContainerName = "charly-build-registry"

	// registryImage is what the local registry is started from. It names the pinned
	// release line rather than a rolling tag, so a build does not silently adopt a new
	// upstream registry behaviour mid-cutover.
	registryImage = "docker.io/library/registry:2"

	// registryContainerPort is the registry's in-container listen port.
	registryContainerPort = "5000"

	// registryProbeTimeout bounds ONE readiness request against the registry API.
	registryProbeTimeout = 2 * time.Second

	// registryReadyWindow is how long a freshly started registry may take to answer /v2/.
	registryReadyWindow = 30 * time.Second

	// registryReadyPoll is the interval between readiness probes.
	registryReadyPoll = 500 * time.Millisecond
)

// ensureLocalRegistry makes the mirror's registry answer before a build needs it. A registry
// already answering /v2/ is reused; otherwise one is started on the mirror's own host:port
// through the CONFIGURED run engine — never a hardcoded one, because the drive resolves
// RunEngine precisely so the operator's engine choice is honoured.
//
// `run --replace` is deliberate: the name is deterministic, so a leftover container from an
// interrupted run must not block the next build, and nothing else is expected to own that
// name (it is the drive's own registry, on the drive's own mirror address).
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
	cmd := exec.CommandContext(ctx, container.EngineBinary(c.RunEngine), "run", "-d",
		"--name", registryContainerName, "--replace",
		"-p", net.JoinHostPort(host, port)+":"+registryContainerPort,
		registryImage)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("starting the local build registry on %s: %w: %s", c.RegistryMirror, err, out)
	}
	deadline := time.Now().Add(registryReadyWindow)
	for {
		if registryAnswers(c.RegistryMirror) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the local build registry on %s did not answer /v2/ within %s", c.RegistryMirror, registryReadyWindow)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(registryReadyPoll):
		}
	}
}

// registryAnswers reports whether something serves the registry API at addr.
func registryAnswers(addr string) bool {
	client := &http.Client{Timeout: registryProbeTimeout}
	resp, err := client.Get("http://" + addr + "/v2/")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
}
