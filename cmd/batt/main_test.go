package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/charlie0129/batt/pkg/compatibility"
)

func TestPprofFlagStartsServer(t *testing.T) {
	previousAddress, previousSocket := pprofAddr, unixSocketPath
	previousClient, previousCapabilities := apiClient, clientCapabilities
	t.Cleanup(func() {
		pprofAddr, unixSocketPath = previousAddress, previousSocket
		apiClient, clientCapabilities = previousClient, previousCapabilities
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := NewCommand()
	cmd.AddCommand(&cobra.Command{
		Use: "profile-probe",
		Run: func(_ *cobra.Command, _ []string) {
			for _, path := range []string{"heap", "profile?seconds=1"} {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/debug/pprof/"+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil || resp.StatusCode != http.StatusOK || len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
					t.Fatalf("%s: expected a gzip profile, got HTTP %d with %d bytes (%v)", path, resp.StatusCode, len(body), err)
				}
			}
		},
	})
	cmd.SetArgs([]string{"profile-probe", "--pprof", address, "--daemon-socket", filepath.Join(t.TempDir(), "absent.sock")})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredCapabilityInheritedFromParentCommand(t *testing.T) {
	root := &cobra.Command{Use: "batt"}
	parent := annotateCapability(&cobra.Command{Use: "adapter"}, compatibility.FeatureAdapterControl)
	child := &cobra.Command{Use: "disable"}
	parent.AddCommand(child)
	root.AddCommand(parent)

	got, ok := requiredCapability(child)
	if !ok || got != compatibility.FeatureAdapterControl {
		t.Fatalf("requiredCapability() = %q, %t", got, ok)
	}
}

func TestHideUnsupportedCommands(t *testing.T) {
	root := NewCommand()
	hideUnsupportedCommands(root, compatibility.Capabilities{
		ChargingControl:   true,
		ChargeControlMode: compatibility.ChargeControlFirmware,
	})

	tests := map[string]bool{
		"limit":                            false,
		"lower-limit-delta":                false,
		"status":                           false,
		"adapter":                          true,
		"prevent-idle-sleep":               true,
		"disable-charging-pre-sleep":       true,
		"prevent-system-sleep":             true,
		"prevent-sleep-on-adapter-disable": true,
		"magsafe-led":                      true,
		"calibration":                      true,
		"schedule":                         true,
	}
	for name, wantHidden := range tests {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if cmd.Hidden != wantHidden {
			t.Errorf("%s hidden = %t, want %t", name, cmd.Hidden, wantHidden)
		}
	}
}
