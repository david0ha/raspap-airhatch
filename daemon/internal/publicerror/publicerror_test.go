package publicerror

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

var allCodes = []Code{WrongPassword, NetworkNotFound, NoDHCPLease, ConnectTimeout, NoSavedNetwork,
	WpaConfUnusable, UnsupportedRaspAPMode, DefaultAPPassphrase, DefaultAdminLogin, NoAPAddress,
	APStartFailed, APServiceOutsideDaemon}

func TestEveryCodeHasAnEnglishMessage(t *testing.T) {
	for _, code := range allCodes {
		e := New(code, "")
		if e.Message == "" || e.Message == string(code) || !strings.HasSuffix(e.Message, ".") {
			t.Errorf("%s: message %q", code, e.Message)
		}
	}
}

func TestSpecExampleMessage(t *testing.T) {
	if got := New(WrongPassword, "Home").Message; got != `The password for "Home" was rejected.` {
		t.Fatal(got)
	}
}

func TestWrapKeepsCauseOutOfMessage(t *testing.T) {
	cause := errors.New("psk=secret")
	err := fmt.Errorf("attempt: %w", Wrap(ConnectTimeout, "Home", cause))
	public := As(err)
	if public == nil || public.Code != ConnectTimeout || strings.Contains(public.Message, "secret") {
		t.Fatalf("%+v", public)
	}
	if !errors.Is(err, cause) {
		t.Fatal("cause not unwrapped")
	}
	if As(errors.New("plain")) != nil {
		t.Fatal("plain error is not public")
	}
}
