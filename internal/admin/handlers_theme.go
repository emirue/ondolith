package admin

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/emirue/ondolith/internal/auth"
	"github.com/emirue/ondolith/internal/content"
	"github.com/emirue/ondolith/internal/theme"
)

// ---- A-212 테마 화면 (D41) ----------------------------------------------------
//
// 활성 테마가 admin.json 으로 선언한 화면을 그리고 값을 저장한다. 어떤 칸이 있고 값이
// 맞는지는 theme.Screen 이 판정한다 — 여기는 권한·본문·사진·저장·기록이다.

// maxThemeSort bounds the 순서 칸. 메뉴처럼 자유로운 정수지만 int32 컬럼을 넘기지 않는다.
const maxThemeSort = 1_000_000

// themeInput is one field as the form draws it.
type themeInput struct {
	Field theme.Field
	// Value 는 입력칸에 보일 값 — 거부된 폼이면 적은 값, 아니면 저장된 값.
	Value string
	// Image 는 사진 칸의 지금 사진이다 (없으면 URL 이 비어 있다).
	Image theme.Image
	// Default 는 placeholder 다 — 폼에 미리 채우면 첫 저장에서 저장값이 된다 (D41 5절).
	Default string
	Error   string
}

// themeRow is one line of a collection's list.
type themeRow struct {
	ID    string
	Label string
	Thumb string
	Sort  int
}

func (d *Deps) themeScreens() []theme.Screen {
	if d.ThemeScreens == nil {
		return nil
	}
	return d.ThemeScreens()
}

func (d *Deps) findThemeScreen(key string) (theme.Screen, bool) {
	for _, s := range d.themeScreens() {
		if s.Key == key {
			return s, true
		}
	}
	return theme.Screen{}, false
}

// themeScreenFor judges the {key} screen: the caller's screen scope first, then
// whether the active theme declares it with the kind this route serves (kind ""
// takes either). 권한을 먼저 본다 — 부여가 없는 사람에게 어떤 화면 키가 있는지
// 404·403 차이로 알려 주지 않는다.
func (d *Deps) themeScreenFor(w http.ResponseWriter, r *http.Request, kind string) (Caller, theme.Screen, bool) {
	c := d.Caller(r)
	key := r.PathValue("key")
	if !c.CanOnScreen(auth.ThemeContentPermission, key) {
		d.forbid(w, r)
		return nil, theme.Screen{}, false
	}
	s, ok := d.findThemeScreen(key)
	if !ok || (kind != "" && s.Kind != kind) {
		d.refuse(w, r, http.StatusNotFound, "찾을 수 없습니다.")
		return nil, theme.Screen{}, false
	}
	return c, s, true
}

// ThemeScreen is A-212's GET: a settings screen's form or a collection's list.
func (d *Deps) ThemeScreen(w http.ResponseWriter, r *http.Request) {
	_, s, ok := d.themeScreenFor(w, r, "")
	if !ok {
		return
	}
	ctx := r.Context()
	if !s.Collection() {
		stored, err := d.Content.ThemeSettings(ctx, s.Key)
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		d.renderThemeForm(w, r, s, http.StatusOK, themeInputs(s, stored, nil, nil), "", nil)
		return
	}
	items, err := d.Content.ThemeItems(ctx, s.Key)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	rows := make([]themeRow, 0, len(items))
	first := s.Fields[0]
	for _, it := range items {
		row := themeRow{ID: it.ID, Sort: it.SortOrder}
		switch v := theme.ScreenValues(s, it.Data)[first.Key].(type) {
		case theme.Image:
			row.Thumb = v.Thumb
		default:
			row.Label = formatThemeValue(v)
		}
		rows = append(rows, row)
	}
	d.Render(w, r, "admin/theme-screen.html", http.StatusOK, map[string]any{
		"Title": s.Title, "Screen": s, "Mode": "list", "Rows": rows,
		"Full": len(items) >= content.MaxThemeItems, "Max": content.MaxThemeItems})
}

// renderThemeForm draws the settings form (item == nil) or a collection row's form.
func (d *Deps) renderThemeForm(w http.ResponseWriter, r *http.Request, s theme.Screen, code int,
	inputs []themeInput, msg string, item *content.ThemeItem) {

	data := map[string]any{"Title": s.Title, "Screen": s, "Mode": "settings", "Inputs": inputs,
		"Multipart": s.ImageFields() > 0, "Action": "/admin/theme/" + s.Key}
	if item != nil {
		data["Mode"] = "item"
		data["Sort"] = item.SortOrder
		data["ItemID"] = item.ID
		if item.ID == "" {
			data["Action"] = "/admin/theme/" + s.Key + "/new"
		} else {
			data["Action"] = "/admin/theme/" + s.Key + "/" + item.ID
		}
	}
	if msg != "" {
		data["Error"] = msg
	}
	d.Render(w, r, "admin/theme-screen.html", code, data)
}

// ThemeScreenSave is A-212's POST for a settings screen.
func (d *Deps) ThemeScreenSave(w http.ResponseWriter, r *http.Request) {
	c, s, ok := d.themeScreenFor(w, r, theme.KindSettings)
	if !ok {
		return
	}
	ctx := r.Context()
	defer removeUploads(r)
	form, files, limits, ok := d.themeForm(w, r, s)
	if !ok {
		return
	}
	prev, err := d.Content.ThemeSettings(ctx, s.Key)
	if err != nil {
		d.serverError(w, r, err)
		return
	}
	out, ok := d.applyThemeForm(w, r, c, s, prev, form, files, limits, nil, "")
	if !ok {
		return
	}
	if err := d.Content.SaveThemeSettings(ctx, s.Key, out); err != nil {
		d.serverError(w, r, err)
		return
	}
	d.log(r, c, "theme.edit_content", "theme_screen", s.Key, s.Title+" 저장")
	http.Redirect(w, r, "/admin/theme/"+s.Key, http.StatusSeeOther)
}

// ThemeItemForm is A-212's GET for a collection row: `/new` or `/{id}`.
func (d *Deps) ThemeItemForm(w http.ResponseWriter, r *http.Request) {
	_, s, ok := d.themeScreenFor(w, r, theme.KindCollection)
	if !ok {
		return
	}
	item, ok := d.themeItemFor(w, r, s)
	if !ok {
		return
	}
	d.renderThemeForm(w, r, s, http.StatusOK, themeInputs(s, item.Data, nil, nil), "", &item)
}

// themeItemFor loads the {id} row of s, or a fresh one (순서 = 맨 뒤) when there is no {id}.
func (d *Deps) themeItemFor(w http.ResponseWriter, r *http.Request, s theme.Screen) (content.ThemeItem, bool) {
	ctx := r.Context()
	id := r.PathValue("id")
	if id == "" {
		n, err := d.Content.NextThemeItemOrder(ctx, s.Key)
		if err != nil {
			d.serverError(w, r, err)
			return content.ThemeItem{}, false
		}
		return content.ThemeItem{SortOrder: n}, true
	}
	item, err := d.Content.ThemeItem(ctx, s.Key, id)
	if d.fail(w, r, err) {
		return content.ThemeItem{}, false
	}
	return item, true
}

// ThemeItemSave is A-212's POST for a collection row: adds on `/new`, rewrites on `/{id}`.
func (d *Deps) ThemeItemSave(w http.ResponseWriter, r *http.Request) {
	c, s, ok := d.themeScreenFor(w, r, theme.KindCollection)
	if !ok {
		return
	}
	ctx := r.Context()
	item, ok := d.themeItemFor(w, r, s)
	if !ok {
		return
	}
	defer removeUploads(r)
	form, files, limits, ok := d.themeForm(w, r, s)
	if !ok {
		return
	}
	creating := item.ID == ""
	if creating {
		// 사진을 저장하기 전에 센다 — 넘친 요청이 사진 파일을 남기지 않게.
		items, err := d.Content.ThemeItems(ctx, s.Key)
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		if len(items) >= content.MaxThemeItems {
			d.renderThemeForm(w, r, s, http.StatusUnprocessableEntity, themeInputs(s, nil, form, nil),
				fmt.Sprintf("항목은 %d개까지입니다.", content.MaxThemeItems), &item)
			return
		}
	}
	sortErr := ""
	if raw := form.Get("_sort"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < -maxThemeSort || n > maxThemeSort {
			sortErr = "순서는 -1,000,000 ~ 1,000,000 사이의 정수입니다."
		} else {
			item.SortOrder = n
		}
	}
	out, ok := d.applyThemeForm(w, r, c, s, item.Data, form, files, limits, &item, sortErr)
	if !ok {
		return
	}
	if creating {
		id, err := d.Content.CreateThemeItem(ctx, s.Key, item.SortOrder, out)
		if errors.Is(err, content.ErrThemeItemLimit) {
			d.renderThemeForm(w, r, s, http.StatusUnprocessableEntity, themeInputs(s, nil, form, nil),
				fmt.Sprintf("항목은 %d개까지입니다.", content.MaxThemeItems), &item)
			return
		}
		if err != nil {
			d.serverError(w, r, err)
			return
		}
		d.log(r, c, "theme.edit_content", "theme_item", id, s.Title+" 항목 추가")
	} else {
		if err := d.Content.UpdateThemeItem(ctx, s.Key, item.ID, item.SortOrder, out); d.fail(w, r, err) {
			return
		}
		d.log(r, c, "theme.edit_content", "theme_item", item.ID, s.Title+" 항목 수정")
	}
	http.Redirect(w, r, "/admin/theme/"+s.Key, http.StatusSeeOther)
}

// ThemeItemDelete is A-212's POST `/{id}/delete`. 사진 파일은 남는다 (D41 1절).
func (d *Deps) ThemeItemDelete(w http.ResponseWriter, r *http.Request) {
	c, s, ok := d.themeScreenFor(w, r, theme.KindCollection)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := d.Content.DeleteThemeItem(r.Context(), s.Key, id); d.fail(w, r, err) {
		return
	}
	d.log(r, c, "theme.edit_content", "theme_item", id, s.Title+" 항목 삭제")
	http.Redirect(w, r, "/admin/theme/"+s.Key, http.StatusSeeOther)
}

// removeUploads deletes the multipart temp files. 처리기가 받는 r 은 미들웨어가 WithContext 로
// 만든 사본이라 서버의 자동 정리(원본 요청만 본다)가 닿지 않는다 — 다른 업로드 처리기와 같다.
func removeUploads(r *http.Request) {
	if r.MultipartForm != nil {
		_ = r.MultipartForm.RemoveAll()
	}
}

// themeForm reads the body under D41 6절's cap — **파싱보다 먼저 건다** (W6-28) —
// and returns the form with the picture files that came. 사진 크기만큼의 상한은 multipart 에만
// 준다: urlencoded 본문은 net/http 가 상한 안에서 통째로 메모리에 읽는다.
func (d *Deps) themeForm(w http.ResponseWriter, r *http.Request, s theme.Screen) (url.Values, map[string]*multipart.FileHeader, content.UploadLimits, bool) {
	limits := content.DefaultUploadLimits()
	bodyCap := int64(1 << 20)
	isMultipart := strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data")
	if s.ImageFields() > 0 && isMultipart {
		l, err := d.Attachments.Limits(r.Context())
		if err != nil {
			d.serverError(w, r, err)
			return nil, nil, limits, false
		}
		limits = l
		bodyCap += int64(s.ImageFields()) * limits.MaxBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, bodyCap)
	// **파서는 하나만 부른다.** ParseMultipartForm 은 urlencoded 본문의 ParseForm 오류(상한 초과)를
	// 들고 있다가 ErrNotMultipart 를 먼저 돌려준다 — 그때 ParseForm 을 다시 부르면 이미 만들어진
	// 빈 폼에 오류 없이 돌아와, 상한을 넘은 본문이 빈 폼으로 처리됐다.
	var err error
	if isMultipart {
		err = r.ParseMultipartForm(8 << 20)
	} else {
		err = r.ParseForm()
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			d.refuse(w, r, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("사진이 너무 큽니다 (한 장에 %d MB 까지).", limits.MaxBytes>>20))
			return nil, nil, limits, false
		}
		d.refuse(w, r, http.StatusBadRequest, "잘못된 요청입니다.")
		return nil, nil, limits, false
	}
	files := map[string]*multipart.FileHeader{}
	if r.MultipartForm != nil {
		for _, f := range s.Fields {
			if fhs := r.MultipartForm.File[f.Key]; f.Type == theme.FieldImage && len(fhs) > 0 {
				files[f.Key] = fhs[0]
			}
		}
	}
	return r.PostForm, files, limits, true
}

// applyThemeForm checks the form, stores the pictures, and returns the merged
// values — or draws the refusal and returns false. **검사가 사진 저장보다 먼저다**:
// 칸 검사에서 거부될 폼은 사진 파일을 남기지 않는다 (theme.Screen.Apply 의 두 번 호출).
// 사진 저장이 시작된 뒤의 실패(둘째 사진 거부·DB 쓰기 실패)는 앞서 저장한 사진을 media 에
// 남긴다 — media 는 지우지 않는 설계다 (D41 1절, D85 GAP-33).
func (d *Deps) applyThemeForm(w http.ResponseWriter, r *http.Request, c Caller, s theme.Screen,
	prev map[string]any, form url.Values, files map[string]*multipart.FileHeader,
	limits content.UploadLimits, item *content.ThemeItem, sortErr string) (map[string]any, bool) {

	refuse := func(errs []theme.FieldError, msg string) {
		inputs := themeInputs(s, prev, form, errs)
		d.renderThemeForm(w, r, s, http.StatusUnprocessableEntity, inputs, msg, item)
	}
	pending := make(map[string]map[string]any, len(files))
	for k := range files {
		pending[k] = nil
	}
	// sortErr 는 모음형 행의 「순서」 칸 오류다 — 선언된 칸이 아니라 Apply 가 모른다.
	if _, errs := s.Apply(prev, form, pending); len(errs) > 0 || sortErr != "" {
		msg := sortErr
		if msg == "" {
			msg = "입력을 확인하세요."
		}
		refuse(errs, msg)
		return nil, false
	}
	pics, err := d.saveThemePictures(r.Context(), s, files, c.UserID(), limits)
	var fe theme.FieldError
	if errors.As(err, &fe) {
		refuse([]theme.FieldError{fe}, fe.Error())
		return nil, false
	}
	if err != nil {
		d.serverError(w, r, err)
		return nil, false
	}
	out, _ := s.Apply(prev, form, pics)
	return out, true
}

// saveThemePictures stores each picture through the editor's pipeline (D41 6절):
// 확장자·내용 검사, 줄이기, EXIF 제거, 썸네일. A refusal the person can act on comes
// back as a theme.FieldError naming the field.
func (d *Deps) saveThemePictures(ctx context.Context, s theme.Screen, files map[string]*multipart.FileHeader,
	uploader string, limits content.UploadLimits) (map[string]map[string]any, error) {

	out := make(map[string]map[string]any, len(files))
	for _, f := range s.Fields {
		fh, ok := files[f.Key]
		if !ok {
			continue
		}
		file, err := fh.Open()
		if err != nil {
			return nil, err
		}
		md, err := d.Media.Save(ctx, uploader, fh.Filename, file, limits, time.Now())
		_ = file.Close()
		if msg := editorMessage(err, limits); msg != "" {
			return nil, theme.FieldError{Key: f.Key, Label: f.Label, Message: msg}
		}
		if err != nil {
			return nil, err
		}
		out[f.Key] = map[string]any{"id": md.ID, "w": md.Width, "h": md.Height}
	}
	return out, nil
}

// themeInputs pairs each field with what its input shows. form is the refused
// submission (nil when drawing stored values).
func themeInputs(s theme.Screen, stored map[string]any, form url.Values, errs []theme.FieldError) []themeInput {
	msgs := make(map[string]string, len(errs))
	for _, e := range errs {
		msgs[e.Key] = e.Message
	}
	view := theme.ScreenValues(s, stored)
	out := make([]themeInput, 0, len(s.Fields))
	for _, f := range s.Fields {
		in := themeInput{Field: f, Default: formatThemeValue(f.Default), Error: msgs[f.Key]}
		switch {
		case f.Type == theme.FieldImage:
			in.Image, _ = view[f.Key].(theme.Image)
		case form != nil:
			in.Value = form.Get(f.Key)
		default:
			in.Value = formatThemeValue(f.Value(stored[f.Key]))
		}
		out = append(out, in)
	}
	return out
}

// formatThemeValue writes a stored or default value back into an input — JSON 숫자는
// 지수 없이 (1000000 이 1e+06 이 되지 않게).
func formatThemeValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}
