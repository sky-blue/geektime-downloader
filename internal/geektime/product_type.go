package geektime

// ProductType geektime product type
type ProductType string

const (
	// ProductTypeColumn 专栏
	ProductTypeColumn ProductType = "c1"
	// ProductTypeNormalVideo 视频课
	ProductTypeNormalVideo ProductType = "c3"
	// ProductTypeC6Video 视频课
	// https://time.geekbang.org/course/intro/100102901
	ProductTypeC6Video ProductType = "c6"
	// ProductTypeDailyLesson 每日一课
	ProductTypeDailyLesson ProductType = "d"
	// ProductTypeQCONPlus 大厂案例
	ProductTypeQCONPlus ProductType = "q"
	// ProductTypeUniversityVideo 训练营视频，自定义类型
	ProductTypeUniversityVideo ProductType = "u"
	// ProductTypeP29
	// https://time.geekbang.org/opencourse/intro/100064201
	ProductTypeP29 ProductType = "p29"
)
