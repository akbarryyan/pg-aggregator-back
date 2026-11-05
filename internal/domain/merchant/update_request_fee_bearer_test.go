package merchant

import (
	"reflect"
	"strings"
	"testing"
)

// UpdateMerchantRequest is also bound by the merchant-facing PUT
// /merchant/business, so it must never carry fee_bearer: that would let a
// merchant choose who pays the provider fee. Admins use a dedicated endpoint.
func TestUpdateMerchantRequest_HasNoFeeBearerField(t *testing.T) {
	typ := reflect.TypeOf(UpdateMerchantRequest{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "feebearer") || strings.Contains(f.Tag.Get("json"), "fee_bearer") {
			t.Fatalf("UpdateMerchantRequest must not expose %s", f.Name)
		}
	}
}
