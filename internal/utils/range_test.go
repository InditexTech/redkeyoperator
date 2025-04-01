package utils

import (
	"reflect"
	"testing"
)

func TestMakeRangeMap(t *testing.T) {
	expected := map[int]interface{}{
		0:  "",
		1:  "",
		2:  "",
		3:  "",
		4:  "",
		5:  "",
		6:  "",
		7:  "",
		8:  "",
		9:  "",
		10: "",
	}
	got := MakeRangeMap(0, 10)
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("Got unexpected map. Got %v, Expected %v", got, expected)
	}
}
