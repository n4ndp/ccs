package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/n4ndp/ccs/internal/claude"
)

// fakeLogin writes what `claude auth login` leaves in an isolated CLAUDE_CONFIG_DIR.
func fakeLogin(t *testing.T, dir, uuid, email, token string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".credentials.json"), creds(token))
	raw, _ := json.Marshal(map[string]any{"oauthAccount": oauth(uuid, email), "firstStartTime": "x"})
	writeFile(t, filepath.Join(dir, ".claude.json"), string(raw))
}

func TestAddCapturesIsolatedLoginWithoutTouchingLive(t *testing.T) {
	f := newFixture(t)
	f.save("work")
	dir, err := f.store.NewLoginDir()
	if err != nil {
		t.Fatal(err)
	}
	if mode(t, dir) != 0o700 {
		t.Fatalf("login dir mode = %v", mode(t, dir))
	}
	fakeLogin(t, dir, "uuid-n", "new@example.com", "n1")
	acc, err := f.store.Add("new", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Email() != "new@example.com" || f.savedCreds("new") != compact(t, creds("n1")) {
		t.Fatalf("added = %+v", acc)
	}
	if f.liveCreds() != compact(t, creds("a1")) {
		t.Fatal("adding an account must not touch the live login")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the temporary login dir must be removed")
	}
}

func TestAddFailures(t *testing.T) {
	tests := []struct {
		name    string
		login   func(t *testing.T, dir string)
		wantErr error
	}{
		{"login cancelled", func(t *testing.T, dir string) {}, ErrNoLogin},
		{"already saved login", func(t *testing.T, dir string) { fakeLogin(t, dir, "uuid-a", "a@example.com", "a9") }, ErrAlreadySaved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.save("work")
			dir, _ := f.store.NewLoginDir()
			tt.login(t, dir)
			if _, err := f.store.Add("new", dir, false); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the temporary login dir must be removed even on failure")
			}
		})
	}
}

func TestReloginRenewsTokens(t *testing.T) {
	t.Run("inactive account", func(t *testing.T) {
		f := newFixture(t)
		f.saveTwo() // active: personal
		dir, _ := f.store.NewLoginDir()
		fakeLogin(t, dir, "uuid-a", "a@example.com", "a2")
		if _, err := f.store.Relogin("work", dir); err != nil {
			t.Fatal(err)
		}
		if f.savedCreds("work") != compact(t, creds("a2")) || f.liveCreds() != compact(t, creds("b1")) {
			t.Fatal("relogin of an inactive account must only update its saved copy")
		}
	})
	t.Run("active account also updates live files", func(t *testing.T) {
		f := newFixture(t)
		f.saveTwo()
		dir, _ := f.store.NewLoginDir()
		fakeLogin(t, dir, "uuid-b", "b@example.com", "b2")
		if _, err := f.store.Relogin("personal", dir); err != nil {
			t.Fatal(err)
		}
		if f.liveCreds() != compact(t, creds("b2")) {
			t.Fatal("relogin of the active account must refresh the live login")
		}
	})
	t.Run("different account is refused", func(t *testing.T) {
		f := newFixture(t)
		f.saveTwo()
		dir, _ := f.store.NewLoginDir()
		fakeLogin(t, dir, "uuid-z", "z@example.com", "z1")
		if _, err := f.store.Relogin("work", dir); !errors.Is(err, ErrWrongAccount) {
			t.Fatalf("err = %v", err)
		}
		if f.savedCreds("work") != compact(t, creds("a1")) {
			t.Fatal("saved credentials must stay untouched")
		}
	})
}

func TestCheckActiveAccountUsesLiveFiles(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	var gotDir *string
	st, err := f.store.Check("personal", func(dir string) (claude.Status, error) {
		gotDir = &dir
		return claude.Status{LoggedIn: true, SubscriptionType: "max"}, nil
	})
	if err != nil || !st.LoggedIn {
		t.Fatalf("status = %+v err = %v", st, err)
	}
	if gotDir == nil || *gotDir != "" {
		t.Fatalf("active account must be checked against the live files, got dir %v", gotDir)
	}
}

func TestCheckNeverSavesDegradedCredentials(t *testing.T) {
	tests := []struct {
		name      string
		written   string
		wantSaved string
		wantIn    bool
	}{
		{
			name:      "claude wiped the tokens after a rejected refresh",
			written:   `{"claudeAiOauth":{"accessToken":"","refreshToken":"","expiresAt":0}}`,
			wantSaved: `{"claudeAiOauth":{"accessToken":"at-old","refreshToken":"rt-old","expiresAt":1000}}`,
			wantIn:    false,
		},
		{
			name:      "expiry moved backwards",
			written:   `{"claudeAiOauth":{"accessToken":"at-x","refreshToken":"rt-x","expiresAt":500}}`,
			wantSaved: `{"claudeAiOauth":{"accessToken":"at-old","refreshToken":"rt-old","expiresAt":1000}}`,
			wantIn:    true,
		},
		{
			name:      "successful refresh",
			written:   `{"claudeAiOauth":{"accessToken":"at-new","refreshToken":"rt-new","expiresAt":2000}}`,
			wantSaved: `{"claudeAiOauth":{"accessToken":"at-new","refreshToken":"rt-new","expiresAt":2000}}`,
			wantIn:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.saveTwo() // active: personal
			writeFile(t, filepath.Join(f.store.Paths.Store, "work", credentialsFile),
				`{"claudeAiOauth":{"accessToken":"at-old","refreshToken":"rt-old","expiresAt":1000}}`)
			st, err := f.store.Check("work", func(dir string) (claude.Status, error) {
				writeFile(t, filepath.Join(dir, ".credentials.json"), tt.written)
				return claude.Status{LoggedIn: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if st.LoggedIn != tt.wantIn {
				t.Fatalf("LoggedIn = %v, want %v", st.LoggedIn, tt.wantIn)
			}
			if f.savedCreds("work") != compact(t, tt.wantSaved) {
				t.Fatalf("saved = %s", f.savedCreds("work"))
			}
		})
	}
}

func TestCheckInactiveAccountUsesCopyAndSavesRefreshedToken(t *testing.T) {
	f := newFixture(t)
	f.saveTwo()
	st, err := f.store.Check("work", func(dir string) (claude.Status, error) {
		if dir == "" {
			t.Fatal("inactive account must be checked in a temporary copy")
		}
		if compact(t, readFile(t, filepath.Join(dir, ".credentials.json"))) != compact(t, creds("a1")) {
			t.Fatal("the copy must hold the saved credentials")
		}
		writeFile(t, filepath.Join(dir, ".credentials.json"), creds("a2")) // claude refreshed it
		return claude.Status{LoggedIn: true}, nil
	})
	if err != nil || !st.LoggedIn {
		t.Fatalf("status = %+v err = %v", st, err)
	}
	if f.savedCreds("work") != compact(t, creds("a2")) {
		t.Fatal("a refreshed token must be saved back")
	}
	if f.liveCreds() != compact(t, creds("b1")) {
		t.Fatal("checking an inactive account must not touch the live login")
	}
	if entries, _ := os.ReadDir(filepath.Join(f.store.Paths.Store, tmpDir)); len(entries) != 0 {
		t.Fatalf("temporary dirs left behind: %d", len(entries))
	}
}
