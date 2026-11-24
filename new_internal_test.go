package wasm

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mandesero/tt-wasm/internal/component/driver"
)

func TestLanguageScaffoldsRender(t *testing.T) {
	for _, language := range driver.Languages() {
		t.Run(language, func(t *testing.T) {
			scaffold, err := newScaffold(language, "", "my-calc", "dist/calc.wasm")
			require.NoError(t, err)

			source := componentSource(language)
			if source == "." {
				source = "main.go"

				require.Contains(t, scaffold.files, "go.mod")
			}

			require.Contains(t, scaffold.files, source)
			require.Contains(t, string(scaffold.files["wit/app.wit"]), "world my-calc")

			for name, data := range scaffold.files {
				require.False(t, bytes.Contains(data, []byte("{{.")), name)
			}
		})
	}
}
