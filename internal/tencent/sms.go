package tencent

import (
	"context"
	"fmt"
	"strings"
)

const smsVersion = "2021-01-11"

// SMS is what Tencent Cloud SMS needs to send a code: the application's
// SDK AppID, an approved signature and an approved verification-code
// template whose variables are the code and, when it has two, the minutes
// it stays valid.
type SMS struct {
	AppID    string `json:"appId"`    // SmsSdkAppId, e.g. 1400000000
	Sign     string `json:"sign"`     // signature text, e.g. 喵面板
	Template string `json:"template"` // template ID
	Params   int    `json:"params"`   // variables in the template: 1 (code) or 2 (code, minutes)
	Region   string `json:"region"`   // ap-guangzhou, ap-beijing or ap-nanjing
}

// SendSMS sends a verification code to one phone number (+8613800000000).
func (c *Client) SendSMS(ctx context.Context, s SMS, phone, code string, minutes int) error {
	params := []string{code}
	if s.Params == 2 {
		params = append(params, fmt.Sprint(minutes))
	}
	region := s.Region
	if region == "" {
		region = "ap-guangzhou"
	}
	var out struct {
		SendStatusSet []struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"SendStatusSet"`
	}
	err := c.CallRegion(ctx, "sms", smsVersion, "SendSms", region, map[string]any{
		"PhoneNumberSet": []string{phone}, "SmsSdkAppId": s.AppID, "SignName": s.Sign,
		"TemplateId": s.Template, "TemplateParamSet": params,
	}, &out)
	if err != nil {
		return err
	}
	// The call succeeds as a whole; each number has its own result.
	if len(out.SendStatusSet) == 0 {
		return fmt.Errorf("腾讯云短信没有返回发送结果")
	}
	if st := out.SendStatusSet[0]; !strings.EqualFold(st.Code, "Ok") {
		return fmt.Errorf("短信没有发出（%s）：%s", st.Code, smsHint(st.Code, st.Message))
	}
	return nil
}

// smsHint says in words what the common refusals mean.
func smsHint(code, msg string) string {
	switch {
	case strings.Contains(code, "SignatureIncorrectOrUnapproved"):
		return "签名不对或还没审核通过"
	case strings.Contains(code, "TemplateIncorrectOrUnapproved"):
		return "模板 ID 不对或还没审核通过"
	case strings.Contains(code, "TemplateParamSetNotMatchApprovedTemplate"):
		return "模板里变量的个数和设置的不一样"
	case strings.Contains(code, "PhoneNumberDailyLimit"), strings.Contains(code, "PhoneNumberOneHourLimit"), strings.Contains(code, "PhoneNumberThirtySecondLimit"):
		return "这个号码发得太频繁，被腾讯云限制了，稍后再试"
	case strings.Contains(code, "InsufficientBalanceInSmsPackage"):
		return "短信套餐包余量不足"
	case strings.Contains(code, "SdkAppIdNotExist"):
		return "SDK AppID 不对"
	case strings.Contains(code, "InvalidParameterValue.IncorrectPhoneNumber"):
		return "手机号格式不对"
	}
	return msg
}
