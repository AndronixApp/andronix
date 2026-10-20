package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// XFCE 4.18's /etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfce4-session.xml (Debian 13), shortened.
const upstreamSession = `<?xml version="1.0" encoding="UTF-8"?>

<channel name="xfce4-session" version="1.0">
  <property name="general" type="empty">
    <property name="FailsafeSessionName" type="string" value="Failsafe"/>
    <property name="LockCommand" type="string" value=""/>
  </property>
  <property name="sessions" type="empty">
    <property name="Failsafe" type="empty">
      <property name="IsFailsafe" type="bool" value="true"/>
      <property name="Count" type="int" value="5"/>
      <property name="Client0_Command" type="array">
        <value type="string" value="xfwm4"/>
      </property>
      <property name="Client0_Priority" type="int" value="15"/>
      <property name="Client0_PerScreen" type="bool" value="false"/>
      <property name="Client1_Command" type="array">
        <value type="string" value="xfsettingsd"/>
      </property>
      <property name="Client1_Priority" type="int" value="20"/>
      <property name="Client1_PerScreen" type="bool" value="false"/>
      <property name="Client2_Command" type="array">
        <value type="string" value="xfce4-panel"/>
      </property>
      <property name="Client2_Priority" type="int" value="25"/>
      <property name="Client2_PerScreen" type="bool" value="false"/>
      <property name="Client3_Command" type="array">
        <value type="string" value="Thunar"/>
        <value type="string" value="--daemon"/>
      </property>
      <property name="Client3_Priority" type="int" value="30"/>
      <property name="Client3_PerScreen" type="bool" value="false"/>
      <property name="Client4_Command" type="array">
        <value type="string" value="xfdesktop"/>
      </property>
      <property name="Client4_Priority" type="int" value="35"/>
      <property name="Client4_PerScreen" type="bool" value="false"/>
    </property>
  </property>
</channel>
`

func sessionLayer(t *testing.T, standalone bool) string {
	root := t.TempDir()
	p := filepath.Join(root, "etc/xdg", sessionChannel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(upstreamSession), 0o644)
	base := filepath.Join(root, "etc/xdg/andronix-standalone")
	if err := writeSessionLayer(root, base, standalone); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(base, sessionChannel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSessionLayerStartsClientsTogether(t *testing.T) {
	out := sessionLayer(t, true)
	for _, want := range []string{
		`name="Count" type="int" value="5"`,
		`name="Client0_Priority" type="int" value="15"`,
		`name="Client1_Priority" type="int" value="15"`,
		`name="Client2_Priority" type="int" value="15"`,
		`name="Client3_Priority" type="int" value="15"`,
		`name="Client4_Priority" type="int" value="15"`,
		`name="SaveOnExit" type="bool" value="false"`,
		`name="FailsafeSessionName" type="string" value="Failsafe"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	// Every client stays, in order: xfwm4, xfsettingsd, xfce4-panel, Thunar's daemon, xfdesktop.
	i0, i1, i2, i3, i4 := strings.Index(out, `"xfwm4"`), strings.Index(out, `"xfsettingsd"`), strings.Index(out, `"xfce4-panel"`), strings.Index(out, `"Thunar"`), strings.Index(out, `"xfdesktop"`)
	if !(i0 >= 0 && i0 < i1 && i1 < i2 && i2 < i3 && i3 < i4) {
		t.Errorf("clients changed:\n%s", out)
	}
}

func TestSessionLayerTermuxKeepsSaving(t *testing.T) {
	if out := sessionLayer(t, false); strings.Contains(out, "SaveOnExit") {
		t.Errorf("Termux layer touches SaveOnExit:\n%s", out)
	}
}

func TestSessionLayerWithoutXfce(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "layer")
	if err := writeSessionLayer(root, base, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, sessionChannel)); err == nil {
		t.Fatal("wrote a session channel for a distro without XFCE's")
	}
}
