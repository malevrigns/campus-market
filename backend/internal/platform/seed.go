package platform

// SeedDemo is explicitly opt-in, never enabled in production. Demo users cannot
// log in and demo listings cannot accept orders or messages.
func (s *Server) SeedDemo() error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT OR IGNORE INTO users VALUES(?,?,?,?,?)", "demo_seller", "showcase@invalid.example", "集市编辑部", "disabled", now()); e != nil {
		return e
	}
	seeds := []struct {
		title, description, category, campus, condition, image string
		price                                                  int64
	}{
		{"无线头戴式降噪耳机", "安静一点，听见自己。轻巧机身，日常听歌与自习。展示图仅作风格参考。", "数码电子", "东区", "九成新", "photo-1546435770-a3e426bf472b", 98000},
		{"复古胶片相机", "把校园生活拍成电影。轻巧机身，适合日常记录。展示图仅作风格参考。", "数码电子", "南湖校区", "九成新", "photo-1516035069371-29a1b244cc32", 268000},
		{"一整个周末的好书", "从设计到生活，给阅读留一些时间。展示图仅作风格参考。", "教材书籍", "西区", "八成新", "photo-1495446815901-a7297e633e8d", 4500},
		{"轻盈日常慢跑鞋", "下一段旅途，从轻盈的脚步开始。展示图仅作风格参考。", "服饰鞋包", "未来城", "九九新", "photo-1542291026-7eec264c27ff", 26000},
		{"Keychron 无线机械键盘", "每一次敲击都刚刚好。清爽桌搭，适合自习与创作。展示图仅作风格参考。", "数码电子", "东区", "九成新", "photo-1587829741301-dc798b83add3", 18000},
		{"暖光台灯 · 宿舍的角落", "小小光源，让每个夜晚都温柔一些。展示图仅作风格参考。", "生活日用", "西区", "九成新", "photo-1507473885765-e6ed057f782c", 6500},
		{"周末骑行 · 城市单车", "下课后，沿着湖边骑一圈。展示图仅作风格参考。", "运动户外", "南湖校区", "八成新", "photo-1485965120184-e220f721d03e", 48000},
		{"日常通勤双肩包", "装下电脑、课本和你的奇思妙想。展示图仅作风格参考。", "服饰鞋包", "东区", "九九新", "photo-1553062407-98eeb64c6a62", 8900},
	}
	for i, p := range seeds {
		pid := []string{"demo_1", "demo_2", "demo_3", "demo_4", "demo_5", "demo_6", "demo_7", "demo_8"}[i]
		_, e = tx.Exec("INSERT OR IGNORE INTO products VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", pid, "demo_seller", p.title, p.description, p.price, p.category, p.campus, p.condition, "https://images.unsplash.com/"+p.image+"?auto=format&fit=crop&w=900&q=85", "available", 1, now()-int64(i)*60000)
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
