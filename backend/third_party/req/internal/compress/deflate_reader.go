package compress

import (
	"compress/flate"
	"io"
)

type DeflateReader struct {
	Body io.ReadCloser // underlying Response.Body
	dr   io.ReadCloser // lazily-initialized deflate reader
	derr error         // sticky error
}

func NewDeflateReader(body io.ReadCloser) *DeflateReader {
	return &DeflateReader{Body: body}
}

func (df *DeflateReader) Read(p []byte) (n int, err error) {
	if df.derr != nil {
		return 0, df.derr
	}
	if df.dr == nil {
		df.dr = flate.NewReader(df.Body)
	}
	return df.dr.Read(p)
}

func (df *DeflateReader) Close() error {
	var decoderErr error
	if df.dr != nil {
		decoderErr = df.dr.Close()
	}
	bodyErr := df.Body.Close()
	if decoderErr != nil {
		return decoderErr
	}
	return bodyErr
}

func (df *DeflateReader) GetUnderlyingBody() io.ReadCloser {
	return df.Body
}

func (df *DeflateReader) SetUnderlyingBody(body io.ReadCloser) {
	df.Body = body
}
