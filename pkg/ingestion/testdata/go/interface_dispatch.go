package sample

type Writer interface {
	Write(data []byte) error
	Flush() error
}

type Reader interface {
	Read(p []byte) (int, error)
}

type CozoDB struct {
	path string
}

func (c *CozoDB) Write(data []byte) error { return nil }
func (c *CozoDB) Flush() error            { return nil }

type FileStore struct {
	dir string
}

func (f *FileStore) Write(data []byte) error { return nil }
func (f *FileStore) Flush() error            { return nil }

type Builder struct {
	writer Writer
	name   string
	reader *Reader
}

func (b *Builder) Build() error {
	b.writer.Write([]byte("data"))
	return b.writer.Flush()
}
