// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseInt safely converts a string to an int.
func ParseInt(value string) int {
	trimmed := strings.TrimSpace(value)
	num, err := strconv.Atoi(trimmed)
	if err != nil {
		fmt.Printf("could not parse int from %q: %v\n", trimmed, err)
		return 0
	}
	return num
}

// ParseInt64 safely converts a string to an int64.
func ParseInt64(value string) int64 {
	trimmed := strings.TrimSpace(value)
	num, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		fmt.Printf("could not parse int64 from %q: %v\n", trimmed, err)
		return 0
	}
	return num
}

// ParseFloat safely converts a string to a float64.
func ParseFloat(value string) float64 {
	trimmed := strings.TrimSpace(value)
	num, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		fmt.Printf("could not parse float64 from %q: %v\n", trimmed, err)
		return 0.0
	}
	return num
}
