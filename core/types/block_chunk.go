package types

import (
	"cmp"
	"slices"
)

func MergeRequests(allRequests [][][]byte) [][]byte {
	res := make([][]byte, 0)

	for _, requests := range allRequests {
		for _, request := range requests {
			if len(request) <= 1 {
				continue
			}

			requestType := request[0]
			requestData := request[1:]

			i := slices.IndexFunc(res,
				func(r []byte) bool { return r[0] == requestType },
			)
			if i == -1 {
				res = append(res, request)
			} else {
				res[i] = append(res[i], requestData...)
			}
		}
	}

	slices.SortFunc(res,
		func(a, b []byte) int { return cmp.Compare(a[0], b[0]) },
	)

	return res
}
