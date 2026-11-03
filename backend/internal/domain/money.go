package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Money 把分格式化为支付宝要求的元字符串。
func Money(ct int64) string { return fmt.Sprintf("%d.%02d", ct/100, ct%100) }

// ParseMoney 严格解析“元”金额为分，拒绝符号、指数、多余小数位与尾随字符。
func ParseMoney(v string) (int64, error) {
	parts := strings.Split(v, ".")
	if len(parts) > 2 || len(parts) == 0 || len(parts[0]) == 0 || len(parts[0]) > 8 {
		return 0, errors.New("invalid amount")
	}
	for _, c := range v {
		if (c < '0' || c > '9') && c != '.' {
			return 0, errors.New("invalid amount")
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, errors.New("invalid decimals")
		}
		fraction, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, err
		}
		if len(parts[1]) == 1 {
			fraction *= 10
		}
	}
	return whole*100 + fraction, nil
}
