package app

import (
	"os"
	"path/filepath"
	"testing"
)

// The SVE fix clears Mesa's stale shader cache for root and every user,
// once per distro (#192).
func TestClearMesaShaderCacheOnce(t *testing.T) {
	root := t.TempDir()
	// Caches for root and two users, plus an unrelated file that must survive.
	seed := map[string]string{
		"root/.cache/mesa_shader_cache/index":     "x",
		"root/.cache/mesa_shader_cache_db/foo":    "x",
		"home/willi/.cache/mesa_shader_cache/a":   "x",
		"home/alex/.cache/mesa_shader_cache_sf/b": "x",
		"home/willi/.cache/keep.txt":              "keep",
	}
	for rel, v := range seed {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(v), 0o644)
	}

	clearMesaShaderCacheOnce(root, nil)

	for _, gone := range []string{
		"root/.cache/mesa_shader_cache", "root/.cache/mesa_shader_cache_db",
		"home/willi/.cache/mesa_shader_cache", "home/alex/.cache/mesa_shader_cache_sf",
	} {
		if _, err := os.Stat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s should have been removed", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "home/willi/.cache/keep.txt")); err != nil {
		t.Error("unrelated cache file was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/andronix/mesa-cache-cleared")); err != nil {
		t.Error("marker not written")
	}

	// Once only: a cache that reappears later is left alone.
	again := filepath.Join(root, "root/.cache/mesa_shader_cache/index")
	os.MkdirAll(filepath.Dir(again), 0o755)
	os.WriteFile(again, []byte("x"), 0o644)
	clearMesaShaderCacheOnce(root, nil)
	if _, err := os.Stat(again); err != nil {
		t.Error("second call cleared again; marker should have skipped it")
	}
}
