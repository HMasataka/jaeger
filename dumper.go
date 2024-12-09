package jaeger

import (
	"bytes"
	"io"
	"net/http"
)

type responseDumper struct {
	http.ResponseWriter

	mw  io.Writer
	buf *bytes.Buffer
}

func newResponseDumper(w http.ResponseWriter) *responseDumper {
	buf := new(bytes.Buffer)
	return &responseDumper{
		ResponseWriter: w,

		mw:  io.MultiWriter(w, buf),
		buf: buf,
	}
}

func (d *responseDumper) Write(b []byte) (int, error) {
	return d.mw.Write(b)
}

func (d *responseDumper) GetResponse() string {
	return d.buf.String()
}
