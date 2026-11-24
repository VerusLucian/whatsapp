package connector

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestValidateLabel(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "  Work  ", want: "Work"},
		{in: "Prywatny 📱", want: "Prywatny 📱"},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "a\nb", wantErr: true},
		{in: "a\tb", wantErr: true},
		{in: "a\u2028b", wantErr: true},
		{in: strings.Repeat("ż", maxLabelLength), want: strings.Repeat("ż", maxLabelLength)},
		{in: strings.Repeat("ż", maxLabelLength+1), wantErr: true},
	}
	for _, tt := range tests {
		got, err := validateLabel(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("validateLabel(%q) = %q, %v; want %q, error=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseLabelArgs(t *testing.T) {
	one := []networkid.UserLoginID{"48111"}
	two := []networkid.UserLoginID{"48111", "31222"}
	tests := []struct {
		name     string
		args     []string
		logins   []networkid.UserLoginID
		wantID   networkid.UserLoginID
		wantRest []string
		wantErr  bool
	}{
		{name: "phone and label", args: []string{"+48 111", "Work"}, logins: two, wantID: "48111", wantRest: []string{"Work"}},
		{name: "phone only", args: []string{"31222"}, logins: two, wantID: "31222", wantRest: []string{}},
		{name: "omitted phone", args: []string{"Work", "phone"}, logins: one, wantID: "48111", wantRest: []string{"Work", "phone"}},
		{name: "omitted phone clear", args: []string{"clear"}, logins: one, wantID: "48111", wantRest: []string{"clear"}},
		{name: "omitted phone many logins", args: []string{"Work"}, logins: two, wantErr: true},
		{name: "unknown phone", args: []string{"999"}, logins: one, wantErr: true},
		{name: "no args", args: nil, logins: one, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLabelArgs(tt.args, tt.logins)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error %v", err, tt.wantErr)
			} else if err != nil {
				return
			}
			if got.LoginID != tt.wantID || strings.Join(got.Rest, "|") != strings.Join(tt.wantRest, "|") {
				t.Errorf("got %+v, want %q %v", got, tt.wantID, tt.wantRest)
			}
		})
	}
}

func TestNameWithLabel(t *testing.T) {
	if got := nameWithLabel("Anna", "Work"); got != "Anna · Work" {
		t.Errorf("got %q", got)
	}
}
