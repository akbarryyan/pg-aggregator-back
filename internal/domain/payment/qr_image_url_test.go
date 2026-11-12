package payment

import "testing"

func TestSandboxQRImageURL(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="
	text := "00020101021226..."
	cases := []struct {
		name string
		p    Payment
		want string
	}{
		{"sandbox png", Payment{Reference: "PAY-1-abc", Environment: EnvironmentSandbox, QRISData: &png}, "https://api.example.com/api/v1/public/payments/by-reference/PAY-1-abc/qr.png"},
		{"reference escaped", Payment{Reference: "PAY 1/x", Environment: EnvironmentSandbox, QRISData: &png}, "https://api.example.com/api/v1/public/payments/by-reference/PAY%201%2Fx/qr.png"},
		{"production", Payment{Reference: "PAY-1", Environment: EnvironmentProduction, QRISData: &png}, ""},
		{"no qr", Payment{Reference: "PAY-1", Environment: EnvironmentSandbox}, ""},
		{"not a png data uri", Payment{Reference: "PAY-1", Environment: EnvironmentSandbox, QRISData: &text}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SandboxQRImageURL(&tc.p, "https://api.example.com/"); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
