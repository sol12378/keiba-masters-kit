package voting

import "testing"

func TestPaperDriverDoesNotRequireEnvironmentCredentials(t *testing.T) {
	t.Setenv("KEIBA_LOGIN_ID", "")
	t.Setenv("KEIBA_PASSWORD", "")
	loginID, password, err := CredentialsFor(DriverPaper)()
	if err != nil {
		t.Fatalf("paper credentials: %v", err)
	}
	if loginID == "" || password == "" {
		t.Fatal("paper credentials must be non-empty so the token lifecycle still runs")
	}
}

func TestLiveDriverStillReadsTheEnvironment(t *testing.T) {
	t.Setenv("KEIBA_LOGIN_ID", "")
	t.Setenv("KEIBA_PASSWORD", "")
	if _, _, err := CredentialsFor(DriverLive)(); err == nil {
		t.Fatal("the live driver must refuse to run without credentials")
	}
	t.Setenv("KEIBA_LOGIN_ID", "student")
	t.Setenv("KEIBA_PASSWORD", "secret")
	loginID, _, err := CredentialsFor(DriverLive)()
	if err != nil || loginID != "student" {
		t.Fatalf("live credentials: id=%q err=%v", loginID, err)
	}
}
