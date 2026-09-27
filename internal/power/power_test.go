package power

import "testing"

func TestCommands(t *testing.T) {
	for _, goos := range []string{"darwin", "windows", "linux"} {
		for _, action := range []Action{Sleep, Shutdown} {
			if cmd, err := command(action, goos); err != nil || len(cmd) == 0 {
				t.Errorf("%s/%s: %v %v", goos, action, cmd, err)
			}
		}
	}
	if _, err := command("reboot", "linux"); err == nil {
		t.Error("unknown action accepted")
	}
	if _, err := command(Sleep, "plan9"); err == nil {
		t.Error("unknown OS accepted")
	}
}
