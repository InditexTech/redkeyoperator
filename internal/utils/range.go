// SPDX-FileCopyrightText: 2025 INDUSTRIA DE DISEÑO TEXTIL, S.A. (INDITEX, S.A.)
//
// SPDX-License-Identifier: Apache-2.0

package utils

func MakeRangeMap(min int, max int) map[int]interface{} {
	result := map[int]interface{}{}
	a := make([]int, max-min+1)
	for i := range a {
		result[min+i] = ""
	}
	return result
}
