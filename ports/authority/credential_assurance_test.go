// SPDX-License-Identifier: AGPL-3.0-only
package authority

import "testing"

func TestCredentialAssuranceOnlyCanonicalPasswordMethods(t *testing.T) {
	for _, v := range [][]string{nil, {}, {"otp"}, {"recovery"}, {"passwordless"}, {"pwd", "pwd"}, {"pwd", "otp", "recovery"}, {"pwd", "provider"}} {
		if ValidCredentialMethods(v) {
			t.Fatal(v)
		}
	}
	for _, v := range [][]string{{"pwd"}, {"pwd", "otp"}, {"recovery", "pwd"}} {
		if !ValidCredentialMethods(v) {
			t.Fatal(v)
		}
	}
}
