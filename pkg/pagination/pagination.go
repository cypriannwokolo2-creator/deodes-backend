package pagination

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// Parse reads page + size from the query string. T056: both `limit` and
// `page_size` are accepted as the size key (frontend sends `limit`, older
// tests use `page_size`); limit is clamped to [1,100], page defaults to 1.
func Parse(c *gin.Context) (page int, limit int, offset int) {
	page = 1
	limit = 20
	if p, err := strconv.Atoi(c.DefaultQuery("page", "1")); err == nil && p > 0 {
		page = p
	}
	rawSize := c.Query("page_size")
	if rawSize == "" {
		rawSize = c.DefaultQuery("limit", "20")
	}
	if size, err := strconv.Atoi(rawSize); err == nil && size > 0 {
		limit = min(size, 100)
	}
	offset = (page - 1) * limit
	return
}

func Defaults(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	limit = min(limit, 100)
	return page, limit
}
