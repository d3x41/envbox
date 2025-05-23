package integration_test

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coder/envbox/integration/integrationtest"
)

func TestDocker_Nvidia(t *testing.T) {
	t.Parallel()
	if val, ok := os.LookupEnv("CODER_TEST_INTEGRATION"); !ok || val != "1" {
		t.Skip("integration tests are skipped unless CODER_TEST_INTEGRATION=1")
	}
	// Only run this test if the nvidia container runtime is detected.
	// Check if the nvidia runtime is available using `docker info`.
	// The docker client doesn't expose this information so we need to fetch it ourselves.
	if !slices.Contains(dockerRuntimes(t), "nvidia") {
		t.Skip("this test requires nvidia runtime to be available")
	}

	t.Run("Ubuntu", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		// Start the envbox container.
		ctID := startEnvboxCmd(ctx, t, integrationtest.UbuntuImage, "root",
			"-v", "/usr/lib/x86_64-linux-gnu:/var/coder/usr/lib",
			"--env", "CODER_ADD_GPU=true",
			"--env", "CODER_USR_LIB_DIR=/var/coder/usr/lib",
			"--runtime=nvidia",
			"--gpus=all",
		)

		// Assert that we can run nvidia-smi in the inner container.
		assertInnerNvidiaSMI(ctx, t, ctID)
	})

	t.Run("Redhat", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		// Start the envbox container.
		ctID := startEnvboxCmd(ctx, t, integrationtest.RedhatImage, "root",
			"-v", "/usr/lib/x86_64-linux-gnu:/var/coder/usr/lib",
			"--env", "CODER_ADD_GPU=true",
			"--env", "CODER_USR_LIB_DIR=/var/coder/usr/lib",
			"--runtime=nvidia",
			"--gpus=all",
		)

		// Assert that we can run nvidia-smi in the inner container.
		assertInnerNvidiaSMI(ctx, t, ctID)

		// Make sure dnf still works. This checks for a regression due to
		// gpuExtraRegex matching `libglib.so` in the outer container.
		// This had a dependency on `libpcre.so.3` which would cause dnf to fail.
		out, err := execContainerCmd(ctx, t, ctID, "docker", "exec", "workspace_cvm", "dnf")
		if !assert.NoError(t, err, "failed to run dnf in the inner container") {
			t.Logf("dnf output:\n%s", strings.TrimSpace(out))
		}

		// Make sure libglib.so is not present in the inner container.
		out, err = execContainerCmd(ctx, t, ctID, "docker", "exec", "workspace_cvm", "ls", "-1", "/usr/lib/x86_64-linux-gnu/libglib*")
		// An error is expected here.
		assert.Error(t, err, "libglib should not be present in the inner container")
		assert.Contains(t, out, "No such file or directory", "libglib should not be present in the inner container")
	})

	t.Run("InnerUsrLibDirOverride", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		// Start the envbox container.
		ctID := startEnvboxCmd(ctx, t, integrationtest.UbuntuImage, "root",
			"-v", "/usr/lib/x86_64-linux-gnu:/var/coder/usr/lib",
			"--env", "CODER_ADD_GPU=true",
			"--env", "CODER_USR_LIB_DIR=/var/coder/usr/lib",
			"--env", "CODER_INNER_USR_LIB_DIR=/usr/lib/coder",
			"--runtime=nvidia",
			"--gpus=all",
		)

		// Assert that the libraries end up in the expected location in the inner
		// container.
		out, err := execContainerCmd(ctx, t, ctID, "docker", "exec", "workspace_cvm", "ls", "-1", "/usr/lib/coder")
		require.NoError(t, err, "inner usr lib dir override failed")
		require.Regexp(t, `(?i)(libgl|nvidia|vulkan|cuda)`, out)
	})

	t.Run("EmptyHostUsrLibDir", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		emptyUsrLibDir := t.TempDir()

		// Start the envbox container.
		ctID := startEnvboxCmd(ctx, t, integrationtest.UbuntuImage, "root",
			"-v", emptyUsrLibDir+":/var/coder/usr/lib",
			"--env", "CODER_ADD_GPU=true",
			"--env", "CODER_USR_LIB_DIR=/var/coder/usr/lib",
			"--runtime=nvidia",
			"--gpus=all",
		)

		ofs := outerFiles(ctx, t, ctID, "/usr/lib/x86_64-linux-gnu/libnv*")
		// Assert invariant: the outer container has the files we expect.
		require.NotEmpty(t, ofs, "failed to list outer container files")
		// Assert that expected files are available in the inner container.
		assertInnerFiles(ctx, t, ctID, "/usr/lib/x86_64-linux-gnu/libnv*", ofs...)
		assertInnerNvidiaSMI(ctx, t, ctID)
	})

	t.Run("CUDASample", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		// Start the envbox container.
		ctID := startEnvboxCmd(ctx, t, integrationtest.CUDASampleImage, "root",
			"-v", "/usr/lib/x86_64-linux-gnu:/var/coder/usr/lib",
			"--env", "CODER_ADD_GPU=true",
			"--env", "CODER_USR_LIB_DIR=/var/coder/usr/lib",
			"--runtime=nvidia",
			"--gpus=all",
		)

		// Assert that we can run nvidia-smi in the inner container.
		assertInnerNvidiaSMI(ctx, t, ctID)

		// Assert that /tmp/vectorAdd runs successfully in the inner container.
		_, err := execContainerCmd(ctx, t, ctID, "docker", "exec", "workspace_cvm", "/tmp/vectorAdd")
		require.NoError(t, err, "failed to run /tmp/vectorAdd in the inner container")
	})
}

// dockerRuntimes returns the list of container runtimes available on the host.
// It does this by running `docker info` and parsing the output.
func dockerRuntimes(t *testing.T) []string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "info", "--format", "{{ range $k, $v := .Runtimes}}{{ println $k }}{{ end }}")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to get docker runtimes: %s", out)
	raw := strings.TrimSpace(string(out))
	return strings.Split(raw, "\n")
}

// outerFiles returns the list of files in the outer container matching the
// given pattern. It does this by running `ls -1` in the outer container.
func outerFiles(ctx context.Context, t *testing.T, containerID, pattern string) []string {
	t.Helper()
	// We need to use /bin/sh -c to avoid the shell interpreting the glob.
	out, err := execContainerCmd(ctx, t, containerID, "/bin/sh", "-c", "ls -1 "+pattern)
	require.NoError(t, err, "failed to list outer container files")
	files := strings.Split(strings.TrimSpace(out), "\n")
	slices.Sort(files)
	return files
}

// assertInnerFiles checks that all the files matching the given pattern exist in the
// inner container.
func assertInnerFiles(ctx context.Context, t *testing.T, containerID, pattern string, expected ...string) {
	t.Helper()

	// Get the list of files in the inner container.
	// We need to use /bin/sh -c to avoid the shell interpreting the glob.
	out, err := execContainerCmd(ctx, t, containerID, "docker", "exec", "workspace_cvm", "/bin/sh", "-c", "ls -1 "+pattern)
	require.NoError(t, err, "failed to list inner container files")
	innerFiles := strings.Split(strings.TrimSpace(out), "\n")

	// Check that the expected files exist in the inner container.
	missingFiles := make([]string, 0)
	for _, expectedFile := range expected {
		if !slices.Contains(innerFiles, expectedFile) {
			missingFiles = append(missingFiles, expectedFile)
		}
	}
	require.Empty(t, missingFiles, "missing files in inner container: %s", strings.Join(missingFiles, ", "))
}

// assertInnerNvidiaSMI checks that nvidia-smi runs successfully in the inner
// container.
func assertInnerNvidiaSMI(ctx context.Context, t *testing.T, containerID string) {
	t.Helper()
	// Assert that we can run nvidia-smi in the inner container.
	out, err := execContainerCmd(ctx, t, containerID, "docker", "exec", "workspace_cvm", "nvidia-smi")
	require.NoError(t, err, "failed to run nvidia-smi in the inner container")
	require.Contains(t, out, "NVIDIA-SMI", "nvidia-smi output does not contain NVIDIA-SMI")
}

// startEnvboxCmd starts the envbox container with the given arguments.
// Ideally we would use ory/dockertest for this, but it doesn't support
// specifying the runtime. We have alternatively used the docker client library,
// but a nice property of using the docker cli is that if a test fails, you can
// just run the command manually to debug!
func startEnvboxCmd(ctx context.Context, t *testing.T, innerImage, innerUser string, addlArgs ...string) (containerID string) {
	t.Helper()

	var (
		tmpDir            = integrationtest.TmpDir(t)
		binds             = integrationtest.DefaultBinds(t, tmpDir)
		cancelCtx, cancel = context.WithCancel(ctx)
	)
	t.Cleanup(cancel)

	// Unfortunately ory/dockertest does not allow us to specify runtime.
	// We're instead going to just run the container directly via the docker cli.
	startEnvboxArgs := []string{
		"run",
		"--detach",
		"--rm",
		"--privileged",
		"--env", "CODER_INNER_IMAGE=" + innerImage,
		"--env", "CODER_INNER_USERNAME=" + innerUser,
	}
	for _, bind := range binds {
		bindParts := []string{bind.Source, bind.Target}
		if bind.ReadOnly {
			bindParts = append(bindParts, "ro")
		}
		startEnvboxArgs = append(startEnvboxArgs, []string{"-v", strings.Join(bindParts, ":")}...)
	}
	startEnvboxArgs = append(startEnvboxArgs, addlArgs...)
	startEnvboxArgs = append(startEnvboxArgs, "envbox:latest", "/envbox", "docker")
	t.Logf("envbox docker cmd: docker %s", strings.Join(startEnvboxArgs, " "))

	// Start the envbox container without attaching.
	startEnvboxCmd := exec.CommandContext(cancelCtx, "docker", startEnvboxArgs...)
	out, err := startEnvboxCmd.CombinedOutput()
	require.NoError(t, err, "failed to start envbox container")
	containerID = strings.TrimSpace(string(out))
	t.Logf("envbox container ID: %s", containerID)
	t.Cleanup(func() {
		if t.Failed() {
			// Dump the logs if the test failed.
			logsCmd := exec.Command("docker", "logs", containerID)
			out, err := logsCmd.CombinedOutput()
			if err != nil {
				t.Logf("failed to read logs: %s", err)
			}
			t.Logf("envbox logs:\n%s", string(out))
		}
		// Stop the envbox container.
		stopEnvboxCmd := exec.Command("docker", "rm", "-f", containerID)
		out, err := stopEnvboxCmd.CombinedOutput()
		if err != nil {
			t.Errorf("failed to stop envbox container: %s", out)
		}
	})

	// Wait for the Docker CVM to come up.
	waitCtx, waitCancel := context.WithTimeout(cancelCtx, 5*time.Minute)
	defer waitCancel()
WAITLOOP:
	for {
		select {
		case <-waitCtx.Done():
			t.Fatal("timed out waiting for inner container to come up")
		default:
			execCmd := exec.CommandContext(cancelCtx, "docker", "exec", containerID, "docker", "inspect", "workspace_cvm")
			out, err := execCmd.CombinedOutput()
			if err != nil {
				t.Logf("waiting for inner container to come up:\n%s", string(out))
				<-time.After(time.Second)
				continue WAITLOOP
			}
			t.Logf("inner container is up")
			break WAITLOOP
		}
	}

	return containerID
}

func execContainerCmd(ctx context.Context, t *testing.T, containerID string, cmdArgs ...string) (string, error) {
	t.Helper()

	execArgs := []string{"exec", containerID}
	execArgs = append(execArgs, cmdArgs...)
	t.Logf("exec cmd: docker %s", strings.Join(execArgs, " "))
	execCmd := exec.CommandContext(ctx, "docker", execArgs...)
	out, err := execCmd.CombinedOutput()
	if err != nil {
		t.Logf("exec cmd failed: %s\n%s", err.Error(), string(out))
	} else {
		t.Logf("exec cmd success: %s", out)
	}
	return strings.TrimSpace(string(out)), err
}
