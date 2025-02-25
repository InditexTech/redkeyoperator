package utils

func MakeRangeMap(min int, max int) map[int]interface{} {
	result := map[int]interface{}{}
	a := make([]int, max-min+1)
	for i := range a {
		result[min+i] = ""
	}
	return result
}
