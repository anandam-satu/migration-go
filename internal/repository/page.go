package repository

import (
	"fmt"
	"math"
)

// Page mirrors Spring Data's Page<T> (0-based page numbers).
type Page[T any] struct {
	Content       []T
	TotalElements int64
	Number        int // 0-based page number
	Size          int
	TotalPages    int
}

func newPage[T any](content []T, total int64, number, size int) *Page[T] {
	p := &Page[T]{Content: content, TotalElements: total, Number: number, Size: size}
	if size > 0 {
		p.TotalPages = int(math.Ceil(float64(total) / float64(size)))
	}
	return p
}

// pageClause renders LIMIT/OFFSET for a PageRequest.
func pageClause(page, size int) string {
	if page < 0 {
		page = 0
	}
	if size <= 0 {
		size = 20
	}
	return fmt.Sprintf("LIMIT %d OFFSET %d", size, page*size)
}

// like adds an arg and returns a case-insensitive LIKE fragment.
func like(args *[]any, column, value string) string {
	*args = append(*args, value)
	idx := len(*args)
	return fmt.Sprintf("LOWER(%s) LIKE LOWER('%%' || $%d || '%%')", column, idx)
}
