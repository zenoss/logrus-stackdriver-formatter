package test

// ErrorAtDepth logs msg at error level after recursing depth times through
// this package, so tests can build stacks of any depth.
func (l *LogWrapper) ErrorAtDepth(depth int, msg string) {
	if depth > 0 {
		l.ErrorAtDepth(depth-1, msg)
		return
	}
	l.Logger.Error(msg)
}
