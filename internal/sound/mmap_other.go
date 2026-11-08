//go:build !linux

package sound

import "os"

func mapFile(path string) ([]byte, func(), error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > 64<<20 {
		return nil, nil, os.ErrInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return data, func() {}, nil
}
