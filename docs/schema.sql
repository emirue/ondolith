-- Ondolith 통합 스키마 — ERD 도구에 올리는 파일.
--
-- **자동 생성이다. 손으로 고치지 말 것.** `make schema` 가 빈 데이터베이스에
-- internal/migrations/*.sql 을 전부 적용한 뒤 pg_dump 로 뽑는다. 여기를 고쳐도
-- 다음 생성에서 사라지고, 그 사이에 이 파일은 거짓말을 한다.
--
-- 스키마를 바꾸려면 마이그레이션을 추가하고 (docs/30-data-model.md),
-- 그다음 `make schema` 를 돌린다.
--
-- 설계 의도·정규화 근거·외래키 정책은 docs/30-data-model.md 가 설명한다.
-- 이 파일은 그 결과물이지 설명이 아니다.
--
-- PostgreSQL database dump
--

\restrict hvLJ9HjDe2rmfPDCNPMG7xMADVSVlUaHZuGNxtAaKnw1UlFGhypp29nfXcwc0H7

-- Dumped from database version 18.4
-- Dumped by pg_dump version 18.4

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: operation_logs_append_only(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.operation_logs_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION '작업 로그는 수정·삭제할 수 없습니다 (D15 7절)';
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: attachments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.attachments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    post_id uuid NOT NULL,
    stored_path text NOT NULL,
    original_name text NOT NULL,
    mime_type text NOT NULL,
    byte_size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT attachments_byte_size_check CHECK ((byte_size > 0)),
    CONSTRAINT attachments_mime_type_check CHECK (((length(mime_type) >= 1) AND (length(mime_type) <= 128))),
    CONSTRAINT attachments_original_name_check CHECK (((length(original_name) >= 1) AND (length(original_name) <= 255))),
    CONSTRAINT attachments_stored_path_check CHECK (((stored_path ~ '^[0-9]{4}/[0-9]{2}/[0-9a-f-]{36}$'::text) AND (length(stored_path) <= 128)))
);


--
-- Name: board_fields; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.board_fields (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    board_id uuid NOT NULL,
    key text NOT NULL,
    label text NOT NULL,
    field_type text NOT NULL,
    is_required boolean DEFAULT false NOT NULL,
    show_in_list boolean DEFAULT false NOT NULL,
    options jsonb DEFAULT '[]'::jsonb NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT board_fields_field_type_check CHECK ((field_type = ANY (ARRAY['text'::text, 'textarea'::text, 'number'::text, 'select'::text, 'checkbox'::text, 'multiselect'::text, 'date'::text, 'url'::text]))),
    CONSTRAINT board_fields_key_check CHECK (((key ~ '^[a-z][a-z0-9_]*$'::text) AND (length(key) <= 32))),
    CONSTRAINT board_fields_label_check CHECK (((length(label) >= 1) AND (length(label) <= 100))),
    CONSTRAINT board_fields_options_shape CHECK (((jsonb_typeof(options) = 'array'::text) AND (octet_length((options)::text) <= 4096))),
    CONSTRAINT board_fields_options_when CHECK (((field_type = ANY (ARRAY['select'::text, 'multiselect'::text])) = (options <> '[]'::jsonb)))
);


--
-- Name: boards; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.boards (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    skin text DEFAULT ''::text NOT NULL,
    allow_attachments boolean DEFAULT false NOT NULL,
    allow_comments boolean DEFAULT true NOT NULL,
    allow_secret boolean DEFAULT false NOT NULL,
    per_page integer DEFAULT 20 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT boards_name_check CHECK (((length(name) >= 1) AND (length(name) <= 100))),
    CONSTRAINT boards_per_page_check CHECK (((per_page >= 1) AND (per_page <= 100))),
    CONSTRAINT boards_skin_check CHECK (((skin = ''::text) OR ((skin ~ '^[a-z0-9][a-z0-9-]*$'::text) AND (length(skin) <= 64)))),
    CONSTRAINT boards_slug_check CHECK (((slug ~ '^[a-z0-9][a-z0-9-]*$'::text) AND (length(slug) <= 64)))
);


--
-- Name: cart_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cart_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    cart_id uuid NOT NULL,
    variant_id uuid NOT NULL,
    quantity integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT cart_items_quantity_check CHECK ((quantity >= 1))
);


--
-- Name: carts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.carts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid,
    guest_key text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT carts_guest_key_check CHECK (((guest_key IS NULL) OR ((length(guest_key) >= 16) AND (length(guest_key) <= 128)))),
    CONSTRAINT carts_owner_is_one CHECK (((user_id IS NULL) <> (guest_key IS NULL)))
);


--
-- Name: categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.categories (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    parent_id uuid,
    name text NOT NULL,
    slug text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT categories_name_check CHECK (((length(name) >= 1) AND (length(name) <= 100))),
    CONSTRAINT categories_no_self_parent CHECK (((parent_id IS NULL) OR (parent_id <> id))),
    CONSTRAINT categories_slug_check CHECK (((slug ~ '^[a-z0-9][a-z0-9-]*$'::text) AND (length(slug) <= 100)))
);


--
-- Name: comments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.comments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    post_id uuid NOT NULL,
    parent_id uuid,
    author_id uuid,
    body text NOT NULL,
    deleted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT comments_body_check CHECK ((length(body) <= 2000)),
    CONSTRAINT comments_no_self_parent CHECK (((parent_id IS NULL) OR (parent_id <> id))),
    CONSTRAINT comments_tombstone_is_empty CHECK (((deleted_at IS NULL) OR (body = ''::text)))
);


--
-- Name: email_verification_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_verification_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: goose_db_version; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.goose_db_version (
    id integer NOT NULL,
    version_id bigint NOT NULL,
    is_applied boolean NOT NULL,
    tstamp timestamp without time zone DEFAULT now() NOT NULL
);


--
-- Name: goose_db_version_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.goose_db_version ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.goose_db_version_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: menus; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.menus (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    title text NOT NULL,
    url text NOT NULL,
    parent_id uuid,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT menus_url_check CHECK ((url ~ '^(/([^/\\].*)?|https?://[^\s]+)$'::text))
);


--
-- Name: operation_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operation_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    actor_user_id uuid,
    actor_email text DEFAULT ''::text NOT NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text,
    summary text DEFAULT ''::text NOT NULL,
    ip inet,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT operation_logs_action_check CHECK ((action ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$'::text)),
    CONSTRAINT operation_logs_actor_email_check CHECK ((length(actor_email) <= 254)),
    CONSTRAINT operation_logs_summary_check CHECK ((length(summary) <= 2000)),
    CONSTRAINT operation_logs_target_id_check CHECK (((target_id IS NULL) OR (length(target_id) <= 255))),
    CONSTRAINT operation_logs_target_type_check CHECK ((target_type ~ '^[a-z][a-z0-9_]*$'::text))
);


--
-- Name: order_agreements; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.order_agreements (
    order_id uuid NOT NULL,
    terms_id uuid NOT NULL,
    agreed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: order_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.order_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    product_id uuid NOT NULL,
    variant_id uuid NOT NULL,
    product_name text NOT NULL,
    option_label text DEFAULT ''::text NOT NULL,
    unit_price integer NOT NULL,
    quantity integer NOT NULL,
    line_amount integer GENERATED ALWAYS AS ((unit_price * quantity)) STORED,
    settled_quantity integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    discount_amount integer DEFAULT 0 NOT NULL,
    CONSTRAINT order_items_discount_amount_check CHECK ((discount_amount >= 0)),
    CONSTRAINT order_items_discount_within_line CHECK ((discount_amount <= (unit_price * quantity))),
    CONSTRAINT order_items_option_label_check CHECK ((length(option_label) <= 200)),
    CONSTRAINT order_items_product_name_check CHECK (((length(product_name) >= 1) AND (length(product_name) <= 200))),
    CONSTRAINT order_items_quantity_check CHECK ((quantity >= 1)),
    CONSTRAINT order_items_settled_range CHECK (((settled_quantity >= 0) AND (settled_quantity <= quantity))),
    CONSTRAINT order_items_unit_price_check CHECK ((unit_price >= 0))
);


--
-- Name: orders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.orders (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_no text NOT NULL,
    user_id uuid,
    status text DEFAULT '결제대기'::text NOT NULL,
    total_amount integer NOT NULL,
    receiver_name text NOT NULL,
    receiver_phone text NOT NULL,
    postcode text NOT NULL,
    address1 text NOT NULL,
    address2 text DEFAULT ''::text NOT NULL,
    delivery_memo text DEFAULT ''::text NOT NULL,
    orderer_email text NOT NULL,
    orderer_phone text NOT NULL,
    delivered_at timestamp with time zone,
    confirmed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    discount_amount integer DEFAULT 0 NOT NULL,
    CONSTRAINT orders_address1_check CHECK (((length(address1) >= 1) AND (length(address1) <= 200))),
    CONSTRAINT orders_address2_check CHECK ((length(address2) <= 200)),
    CONSTRAINT orders_delivery_memo_check CHECK ((length(delivery_memo) <= 200)),
    CONSTRAINT orders_discount_amount_check CHECK ((discount_amount >= 0)),
    CONSTRAINT orders_email_present CHECK ((orderer_email <> ''::text)),
    CONSTRAINT orders_order_no_check CHECK (((length(order_no) >= 6) AND (length(order_no) <= 32))),
    CONSTRAINT orders_orderer_email_check CHECK (((length(orderer_email) >= 3) AND (length(orderer_email) <= 254))),
    CONSTRAINT orders_orderer_phone_check CHECK (((length(orderer_phone) >= 1) AND (length(orderer_phone) <= 20))),
    CONSTRAINT orders_postcode_check CHECK (((length(postcode) >= 1) AND (length(postcode) <= 10))),
    CONSTRAINT orders_receiver_name_check CHECK (((length(receiver_name) >= 1) AND (length(receiver_name) <= 100))),
    CONSTRAINT orders_receiver_phone_check CHECK (((length(receiver_phone) >= 1) AND (length(receiver_phone) <= 20))),
    CONSTRAINT orders_status_known CHECK ((status = ANY (ARRAY['결제대기'::text, '입금대기'::text, '결제완료'::text, '결제실패'::text, '배송준비'::text, '배송중'::text, '배송완료'::text, '구매확정'::text, '취소'::text, '환불'::text, '반품접수'::text, '반품수거'::text, '교환접수'::text, '교환수거'::text, '차액결제대기'::text, '교환발송'::text]))),
    CONSTRAINT orders_total_amount_check CHECK ((total_amount >= 0))
);


--
-- Name: pages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    template text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pages_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text])))
);


--
-- Name: password_reset_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.password_reset_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: payments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    return_id uuid,
    kind text NOT NULL,
    status text DEFAULT '대기'::text NOT NULL,
    pg text NOT NULL,
    payment_key text NOT NULL,
    approved_amount integer NOT NULL,
    refunded_amount integer DEFAULT 0 NOT NULL,
    raw_response jsonb,
    approved_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    secret text,
    CONSTRAINT payments_approved_amount_check CHECK ((approved_amount >= 0)),
    CONSTRAINT payments_exchange_has_return CHECK (((kind = '교환차액'::text) = (return_id IS NOT NULL))),
    CONSTRAINT payments_kind_known CHECK ((kind = ANY (ARRAY['주문결제'::text, '교환차액'::text]))),
    CONSTRAINT payments_payment_key_check CHECK (((length(payment_key) >= 1) AND (length(payment_key) <= 200))),
    CONSTRAINT payments_pg_check CHECK (((length(pg) >= 1) AND (length(pg) <= 32))),
    CONSTRAINT payments_refund_within_approved CHECK (((refunded_amount >= 0) AND (refunded_amount <= approved_amount))),
    CONSTRAINT payments_secret_check CHECK (((secret IS NULL) OR (length(secret) <= 200))),
    CONSTRAINT payments_status_known CHECK ((status = ANY (ARRAY['대기'::text, '승인'::text, '실패'::text])))
);


--
-- Name: permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.permissions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    key text NOT NULL,
    description text NOT NULL,
    is_scoped boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT permissions_key_check CHECK ((key ~ '^[a-z][a-z0-9]*\.[a-z][a-z0-9_]*$'::text))
);


--
-- Name: posts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.posts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    board_id uuid NOT NULL,
    author_id uuid,
    title text NOT NULL,
    body text NOT NULL,
    custom_fields jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'published'::text NOT NULL,
    is_pinned boolean DEFAULT false NOT NULL,
    is_secret boolean DEFAULT false NOT NULL,
    view_count integer DEFAULT 0 NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, ((title || ' '::text) || body))) STORED,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT posts_body_check CHECK ((length(body) <= 50000)),
    CONSTRAINT posts_custom_fields_shape CHECK (((jsonb_typeof(custom_fields) = 'object'::text) AND (octet_length((custom_fields)::text) <= 16384))),
    CONSTRAINT posts_status_check CHECK ((status = ANY (ARRAY['published'::text, 'hidden'::text]))),
    CONSTRAINT posts_title_check CHECK (((length(title) >= 1) AND (length(title) <= 200))),
    CONSTRAINT posts_view_count_check CHECK ((view_count >= 0))
);


--
-- Name: product_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.product_categories (
    product_id uuid NOT NULL,
    category_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: product_options; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.product_options (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    product_id uuid NOT NULL,
    name text NOT NULL,
    "values" jsonb NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT product_options_name_check CHECK (((length(name) >= 1) AND (length(name) <= 50))),
    CONSTRAINT product_options_values_shape CHECK (((jsonb_typeof("values") = 'array'::text) AND ((jsonb_array_length("values") >= 1) AND (jsonb_array_length("values") <= 50))))
);


--
-- Name: product_variants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.product_variants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    product_id uuid NOT NULL,
    option_values jsonb NOT NULL,
    sku text,
    price_delta integer DEFAULT 0 NOT NULL,
    stock integer DEFAULT 0 NOT NULL,
    is_visible boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT product_variants_option_values_shape CHECK (((jsonb_typeof(option_values) = 'object'::text) AND (octet_length((option_values)::text) <= 4096))),
    CONSTRAINT product_variants_sku_check CHECK (((sku IS NULL) OR ((length(sku) >= 1) AND (length(sku) <= 64)))),
    CONSTRAINT product_variants_stock_check CHECK ((stock >= 0))
);


--
-- Name: products; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.products (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    base_price integer NOT NULL,
    is_visible boolean DEFAULT false NOT NULL,
    search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, ((name || ' '::text) || description))) STORED,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT products_base_price_check CHECK ((base_price >= 0)),
    CONSTRAINT products_description_check CHECK ((length(description) <= 20000)),
    CONSTRAINT products_name_check CHECK (((length(name) >= 1) AND (length(name) <= 200))),
    CONSTRAINT products_slug_check CHECK (((slug ~ '^[a-z0-9][a-z0-9-]*$'::text) AND (length(slug) <= 100)))
);


--
-- Name: refund_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.refund_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    refund_id uuid NOT NULL,
    order_item_id uuid NOT NULL,
    quantity integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT refund_items_quantity_check CHECK ((quantity >= 1))
);


--
-- Name: refunds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.refunds (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    payment_id uuid NOT NULL,
    return_id uuid,
    status text DEFAULT '요청'::text NOT NULL,
    requester text NOT NULL,
    amount integer NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    request_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT refunds_amount_check CHECK ((amount > 0)),
    CONSTRAINT refunds_reason_check CHECK ((length(reason) <= 500)),
    CONSTRAINT refunds_request_key_check CHECK (((length(request_key) >= 1) AND (length(request_key) <= 100))),
    CONSTRAINT refunds_requester_known CHECK ((requester = ANY (ARRAY['구매자'::text, '관리자'::text]))),
    CONSTRAINT refunds_status_known CHECK ((status = ANY (ARRAY['요청'::text, '승인'::text, '거부'::text, '완료'::text])))
);


--
-- Name: return_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.return_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    return_id uuid NOT NULL,
    order_item_id uuid NOT NULL,
    quantity integer NOT NULL,
    is_open boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT return_items_quantity_check CHECK ((quantity >= 1))
);


--
-- Name: returns; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.returns (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    return_no text NOT NULL,
    order_id uuid NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    reject_reason text DEFAULT ''::text NOT NULL,
    fault text,
    shipping_fee_policy text,
    shipping_fee_amount integer,
    new_variant_id uuid,
    price_difference integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT returns_diff_positive_when_pending CHECK (((status <> '차액결제대기'::text) OR (price_difference > 0))),
    CONSTRAINT returns_exchange_needs_variant CHECK (((kind <> '교환'::text) OR (new_variant_id IS NOT NULL))),
    CONSTRAINT returns_exchange_only_fields CHECK (((kind = '교환'::text) OR ((new_variant_id IS NULL) AND (price_difference IS NULL)))),
    CONSTRAINT returns_fault_known CHECK (((fault IS NULL) OR (fault = ANY (ARRAY['구매자'::text, '판매자'::text])))),
    CONSTRAINT returns_fee_amount_range CHECK (((shipping_fee_amount IS NULL) OR (shipping_fee_amount >= 0))),
    CONSTRAINT returns_fee_policy_known CHECK (((shipping_fee_policy IS NULL) OR (shipping_fee_policy = ANY (ARRAY['차감'::text, '별도청구'::text])))),
    CONSTRAINT returns_kind_known CHECK ((kind = ANY (ARRAY['반품'::text, '교환'::text]))),
    CONSTRAINT returns_reason_check CHECK ((length(reason) <= 500)),
    CONSTRAINT returns_reject_reason_check CHECK ((length(reject_reason) <= 500)),
    CONSTRAINT returns_return_no_check CHECK (((length(return_no) >= 6) AND (length(return_no) <= 32))),
    CONSTRAINT returns_seller_fault_free CHECK (((fault <> '판매자'::text) OR (shipping_fee_amount = 0))),
    CONSTRAINT returns_snapshot_after_pickup CHECK (((kind <> '반품'::text) OR (status <> ALL (ARRAY['반품수거'::text, '환불'::text])) OR ((fault IS NOT NULL) AND (shipping_fee_policy IS NOT NULL) AND (shipping_fee_amount IS NOT NULL)))),
    CONSTRAINT returns_status_matches_kind CHECK ((((kind = '반품'::text) AND (status = ANY (ARRAY['반품접수'::text, '반품수거'::text, '환불'::text, '거부'::text]))) OR ((kind = '교환'::text) AND (status = ANY (ARRAY['교환접수'::text, '교환수거'::text, '차액결제대기'::text, '교환발송'::text, '거부'::text])))))
);


--
-- Name: role_permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.role_permissions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    role_id uuid NOT NULL,
    permission_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    board_id uuid
);


--
-- Name: roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.roles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    key text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    is_builtin boolean DEFAULT false NOT NULL,
    is_superuser boolean DEFAULT false NOT NULL,
    is_assignable boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT roles_key_check CHECK ((key ~ '^[a-z][a-z0-9_]*$'::text))
);


--
-- Name: sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sessions (
    token text NOT NULL,
    data bytea NOT NULL,
    expiry timestamp with time zone NOT NULL
);


--
-- Name: settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings (
    key text NOT NULL,
    value text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: shipments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.shipments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    order_id uuid NOT NULL,
    return_id uuid,
    kind text NOT NULL,
    carrier text NOT NULL,
    tracking_no text NOT NULL,
    shipped_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT shipments_carrier_check CHECK (((length(carrier) >= 1) AND (length(carrier) <= 32))),
    CONSTRAINT shipments_exchange_has_return CHECK (((kind = '교환재발송'::text) = (return_id IS NOT NULL))),
    CONSTRAINT shipments_kind_known CHECK ((kind = ANY (ARRAY['최초발송'::text, '교환재발송'::text]))),
    CONSTRAINT shipments_tracking_no_check CHECK (((length(tracking_no) >= 1) AND (length(tracking_no) <= 64)))
);


--
-- Name: social_accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.social_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    provider_uid text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: terms; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.terms (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind text NOT NULL,
    version text NOT NULL,
    body text NOT NULL,
    effective_at timestamp with time zone NOT NULL,
    is_required boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT terms_body_check CHECK (((length(body) >= 1) AND (length(body) <= 20000))),
    CONSTRAINT terms_kind_check CHECK (((length(kind) >= 1) AND (length(kind) <= 50))),
    CONSTRAINT terms_no_backdate CHECK ((effective_at >= created_at)),
    CONSTRAINT terms_version_check CHECK (((length(version) >= 1) AND (length(version) <= 20)))
);


--
-- Name: user_fields; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_fields (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    key text NOT NULL,
    label text NOT NULL,
    field_type text NOT NULL,
    is_required boolean DEFAULT false NOT NULL,
    show_in_list boolean DEFAULT false NOT NULL,
    options jsonb DEFAULT '[]'::jsonb NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_fields_field_type_check CHECK ((field_type = ANY (ARRAY['text'::text, 'textarea'::text, 'number'::text, 'select'::text, 'checkbox'::text, 'multiselect'::text, 'date'::text, 'url'::text]))),
    CONSTRAINT user_fields_key_check CHECK (((key ~ '^[a-z][a-z0-9_]*$'::text) AND (length(key) <= 32))),
    CONSTRAINT user_fields_label_check CHECK (((length(label) >= 1) AND (length(label) <= 100)))
);


--
-- Name: user_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_roles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    role_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    sessions_valid_from timestamp with time zone DEFAULT now() NOT NULL,
    email_verified_at timestamp with time zone,
    custom_fields jsonb DEFAULT '{}'::jsonb NOT NULL
);


--
-- Name: webhook_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    pg text NOT NULL,
    event_id text NOT NULL,
    order_id uuid,
    status text DEFAULT '수신'::text NOT NULL,
    payload jsonb NOT NULL,
    error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT webhook_events_error_check CHECK (((error IS NULL) OR (length(error) <= 500))),
    CONSTRAINT webhook_events_event_id_check CHECK (((length(event_id) >= 1) AND (length(event_id) <= 200))),
    CONSTRAINT webhook_events_pg_check CHECK (((length(pg) >= 1) AND (length(pg) <= 32))),
    CONSTRAINT webhook_events_status_known CHECK ((status = ANY (ARRAY['수신'::text, '처리완료'::text, '실패'::text])))
);


--
-- Name: attachments attachments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_pkey PRIMARY KEY (id);


--
-- Name: attachments attachments_stored_path_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_stored_path_key UNIQUE (stored_path);


--
-- Name: board_fields board_fields_key_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.board_fields
    ADD CONSTRAINT board_fields_key_uniq UNIQUE (board_id, key);


--
-- Name: board_fields board_fields_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.board_fields
    ADD CONSTRAINT board_fields_pkey PRIMARY KEY (id);


--
-- Name: boards boards_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.boards
    ADD CONSTRAINT boards_pkey PRIMARY KEY (id);


--
-- Name: boards boards_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.boards
    ADD CONSTRAINT boards_slug_key UNIQUE (slug);


--
-- Name: cart_items cart_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_items
    ADD CONSTRAINT cart_items_pkey PRIMARY KEY (id);


--
-- Name: cart_items cart_items_variant_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_items
    ADD CONSTRAINT cart_items_variant_uniq UNIQUE (cart_id, variant_id);


--
-- Name: carts carts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.carts
    ADD CONSTRAINT carts_pkey PRIMARY KEY (id);


--
-- Name: categories categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_pkey PRIMARY KEY (id);


--
-- Name: categories categories_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_slug_key UNIQUE (slug);


--
-- Name: comments comments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT comments_pkey PRIMARY KEY (id);


--
-- Name: email_verification_tokens email_verification_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_verification_tokens
    ADD CONSTRAINT email_verification_tokens_pkey PRIMARY KEY (id);


--
-- Name: email_verification_tokens email_verification_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_verification_tokens
    ADD CONSTRAINT email_verification_tokens_token_hash_key UNIQUE (token_hash);


--
-- Name: goose_db_version goose_db_version_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.goose_db_version
    ADD CONSTRAINT goose_db_version_pkey PRIMARY KEY (id);


--
-- Name: menus menus_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.menus
    ADD CONSTRAINT menus_pkey PRIMARY KEY (id);


--
-- Name: operation_logs operation_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operation_logs
    ADD CONSTRAINT operation_logs_pkey PRIMARY KEY (id);


--
-- Name: order_agreements order_agreements_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_agreements
    ADD CONSTRAINT order_agreements_pkey PRIMARY KEY (order_id, terms_id);


--
-- Name: order_items order_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_pkey PRIMARY KEY (id);


--
-- Name: orders orders_order_no_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_order_no_key UNIQUE (order_no);


--
-- Name: orders orders_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);


--
-- Name: pages pages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pages
    ADD CONSTRAINT pages_pkey PRIMARY KEY (id);


--
-- Name: pages pages_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pages
    ADD CONSTRAINT pages_slug_key UNIQUE (slug);


--
-- Name: password_reset_tokens password_reset_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_pkey PRIMARY KEY (id);


--
-- Name: password_reset_tokens password_reset_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_token_hash_key UNIQUE (token_hash);


--
-- Name: payments payments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_pkey PRIMARY KEY (id);


--
-- Name: permissions permissions_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permissions
    ADD CONSTRAINT permissions_key_key UNIQUE (key);


--
-- Name: permissions permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.permissions
    ADD CONSTRAINT permissions_pkey PRIMARY KEY (id);


--
-- Name: posts posts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.posts
    ADD CONSTRAINT posts_pkey PRIMARY KEY (id);


--
-- Name: product_categories product_categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_categories
    ADD CONSTRAINT product_categories_pkey PRIMARY KEY (product_id, category_id);


--
-- Name: product_options product_options_name_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_options
    ADD CONSTRAINT product_options_name_uniq UNIQUE (product_id, name);


--
-- Name: product_options product_options_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_options
    ADD CONSTRAINT product_options_pkey PRIMARY KEY (id);


--
-- Name: product_variants product_variants_combo_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_variants
    ADD CONSTRAINT product_variants_combo_uniq UNIQUE (product_id, option_values);


--
-- Name: product_variants product_variants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_variants
    ADD CONSTRAINT product_variants_pkey PRIMARY KEY (id);


--
-- Name: products products_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_pkey PRIMARY KEY (id);


--
-- Name: products products_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_slug_key UNIQUE (slug);


--
-- Name: refund_items refund_items_item_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refund_items
    ADD CONSTRAINT refund_items_item_uniq UNIQUE (refund_id, order_item_id);


--
-- Name: refund_items refund_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refund_items
    ADD CONSTRAINT refund_items_pkey PRIMARY KEY (id);


--
-- Name: refunds refunds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refunds
    ADD CONSTRAINT refunds_pkey PRIMARY KEY (id);


--
-- Name: refunds refunds_request_key_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refunds
    ADD CONSTRAINT refunds_request_key_uniq UNIQUE (request_key);


--
-- Name: return_items return_items_item_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.return_items
    ADD CONSTRAINT return_items_item_uniq UNIQUE (return_id, order_item_id);


--
-- Name: return_items return_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.return_items
    ADD CONSTRAINT return_items_pkey PRIMARY KEY (id);


--
-- Name: returns returns_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.returns
    ADD CONSTRAINT returns_pkey PRIMARY KEY (id);


--
-- Name: returns returns_return_no_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.returns
    ADD CONSTRAINT returns_return_no_key UNIQUE (return_no);


--
-- Name: role_permissions role_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_pkey PRIMARY KEY (id);


--
-- Name: role_permissions role_permissions_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_uniq UNIQUE NULLS NOT DISTINCT (role_id, permission_id, board_id);


--
-- Name: roles roles_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_key_key UNIQUE (key);


--
-- Name: roles roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (id);


--
-- Name: sessions sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (token);


--
-- Name: settings settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (key);


--
-- Name: shipments shipments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.shipments
    ADD CONSTRAINT shipments_pkey PRIMARY KEY (id);


--
-- Name: social_accounts social_accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_accounts
    ADD CONSTRAINT social_accounts_pkey PRIMARY KEY (id);


--
-- Name: social_accounts social_accounts_provider_uid_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_accounts
    ADD CONSTRAINT social_accounts_provider_uid_uniq UNIQUE (provider, provider_uid);


--
-- Name: social_accounts social_accounts_user_provider_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_accounts
    ADD CONSTRAINT social_accounts_user_provider_uniq UNIQUE (user_id, provider);


--
-- Name: terms terms_kind_version_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terms
    ADD CONSTRAINT terms_kind_version_uniq UNIQUE (kind, version);


--
-- Name: terms terms_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.terms
    ADD CONSTRAINT terms_pkey PRIMARY KEY (id);


--
-- Name: user_fields user_fields_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_fields
    ADD CONSTRAINT user_fields_key_key UNIQUE (key);


--
-- Name: user_fields user_fields_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_fields
    ADD CONSTRAINT user_fields_pkey PRIMARY KEY (id);


--
-- Name: user_roles user_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_roles
    ADD CONSTRAINT user_roles_pkey PRIMARY KEY (id);


--
-- Name: user_roles user_roles_uniq; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_roles
    ADD CONSTRAINT user_roles_uniq UNIQUE (user_id, role_id);


--
-- Name: users users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: webhook_events webhook_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_events
    ADD CONSTRAINT webhook_events_pkey PRIMARY KEY (id);


--
-- Name: attachments_post_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX attachments_post_id_idx ON public.attachments USING btree (post_id);


--
-- Name: cart_items_variant_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX cart_items_variant_idx ON public.cart_items USING btree (variant_id);


--
-- Name: carts_guest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX carts_guest_idx ON public.carts USING btree (guest_key) WHERE (guest_key IS NOT NULL);


--
-- Name: carts_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX carts_user_idx ON public.carts USING btree (user_id) WHERE (user_id IS NOT NULL);


--
-- Name: categories_parent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX categories_parent_idx ON public.categories USING btree (parent_id, sort_order) WHERE (parent_id IS NOT NULL);


--
-- Name: comments_author_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX comments_author_id_idx ON public.comments USING btree (author_id);


--
-- Name: comments_parent_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX comments_parent_id_idx ON public.comments USING btree (parent_id) WHERE (parent_id IS NOT NULL);


--
-- Name: comments_post_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX comments_post_id_idx ON public.comments USING btree (post_id, created_at);


--
-- Name: email_verification_tokens_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX email_verification_tokens_user_id_idx ON public.email_verification_tokens USING btree (user_id);


--
-- Name: menus_parent_sort_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX menus_parent_sort_idx ON public.menus USING btree (parent_id, sort_order);


--
-- Name: operation_logs_actor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX operation_logs_actor_idx ON public.operation_logs USING btree (actor_user_id);


--
-- Name: operation_logs_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX operation_logs_created_at_idx ON public.operation_logs USING btree (created_at DESC);


--
-- Name: order_agreements_terms_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX order_agreements_terms_idx ON public.order_agreements USING btree (terms_id);


--
-- Name: order_items_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX order_items_order_idx ON public.order_items USING btree (order_id);


--
-- Name: order_items_product_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX order_items_product_idx ON public.order_items USING btree (product_id);


--
-- Name: order_items_variant_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX order_items_variant_idx ON public.order_items USING btree (variant_id);


--
-- Name: orders_delivered_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX orders_delivered_idx ON public.orders USING btree (delivered_at) WHERE (status = '배송완료'::text);


--
-- Name: orders_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX orders_status_idx ON public.orders USING btree (status, created_at DESC);


--
-- Name: orders_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX orders_user_idx ON public.orders USING btree (user_id, created_at DESC);


--
-- Name: password_reset_tokens_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX password_reset_tokens_user_id_idx ON public.password_reset_tokens USING btree (user_id);


--
-- Name: payments_exchange_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_exchange_idx ON public.payments USING btree (order_id, return_id) WHERE (kind = '교환차액'::text);


--
-- Name: payments_order_approved_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_order_approved_idx ON public.payments USING btree (order_id) WHERE ((kind = '주문결제'::text) AND (status <> '실패'::text));


--
-- Name: payments_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_pending_idx ON public.payments USING btree (status, created_at) WHERE (status = '대기'::text);


--
-- Name: payments_pg_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_pg_key_idx ON public.payments USING btree (pg, payment_key);


--
-- Name: payments_return_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_return_idx ON public.payments USING btree (return_id) WHERE (return_id IS NOT NULL);


--
-- Name: posts_author_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX posts_author_id_idx ON public.posts USING btree (author_id);


--
-- Name: posts_board_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX posts_board_list_idx ON public.posts USING btree (board_id, is_pinned DESC, created_at DESC, id DESC);


--
-- Name: posts_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX posts_search_idx ON public.posts USING gin (search_vector);


--
-- Name: product_categories_category_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX product_categories_category_idx ON public.product_categories USING btree (category_id);


--
-- Name: product_variants_sellable_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX product_variants_sellable_idx ON public.product_variants USING btree (product_id) WHERE (is_visible AND (stock > 0));


--
-- Name: product_variants_sku_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX product_variants_sku_idx ON public.product_variants USING btree (sku) WHERE (sku IS NOT NULL);


--
-- Name: products_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX products_search_idx ON public.products USING gin (search_tsv);


--
-- Name: products_visible_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX products_visible_idx ON public.products USING btree (is_visible, created_at DESC);


--
-- Name: refund_items_order_item_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX refund_items_order_item_idx ON public.refund_items USING btree (order_item_id);


--
-- Name: refunds_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX refunds_order_idx ON public.refunds USING btree (order_id, created_at DESC);


--
-- Name: refunds_payment_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX refunds_payment_idx ON public.refunds USING btree (payment_id);


--
-- Name: refunds_return_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX refunds_return_idx ON public.refunds USING btree (return_id) WHERE (return_id IS NOT NULL);


--
-- Name: return_items_open_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX return_items_open_idx ON public.return_items USING btree (order_item_id) WHERE is_open;


--
-- Name: return_items_return_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX return_items_return_idx ON public.return_items USING btree (return_id);


--
-- Name: returns_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX returns_order_idx ON public.returns USING btree (order_id, created_at DESC);


--
-- Name: returns_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX returns_status_idx ON public.returns USING btree (status, created_at DESC);


--
-- Name: role_permissions_board_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX role_permissions_board_id_idx ON public.role_permissions USING btree (board_id) WHERE (board_id IS NOT NULL);


--
-- Name: roles_one_superuser_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX roles_one_superuser_idx ON public.roles USING btree (is_superuser) WHERE is_superuser;


--
-- Name: sessions_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX sessions_expiry_idx ON public.sessions USING btree (expiry);


--
-- Name: shipments_first_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX shipments_first_idx ON public.shipments USING btree (order_id) WHERE (kind = '최초발송'::text);


--
-- Name: shipments_return_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX shipments_return_idx ON public.shipments USING btree (return_id) WHERE (return_id IS NOT NULL);


--
-- Name: terms_kind_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX terms_kind_idx ON public.terms USING btree (kind, effective_at DESC);


--
-- Name: user_roles_role_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_roles_role_id_idx ON public.user_roles USING btree (role_id);


--
-- Name: webhook_events_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_events_order_idx ON public.webhook_events USING btree (order_id) WHERE (order_id IS NOT NULL);


--
-- Name: webhook_events_pg_event_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX webhook_events_pg_event_idx ON public.webhook_events USING btree (pg, event_id);


--
-- Name: webhook_events_unhandled_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_events_unhandled_idx ON public.webhook_events USING btree (status, created_at DESC) WHERE (status <> '처리완료'::text);


--
-- Name: operation_logs operation_logs_no_delete; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER operation_logs_no_delete BEFORE DELETE ON public.operation_logs FOR EACH ROW EXECUTE FUNCTION public.operation_logs_append_only();


--
-- Name: operation_logs operation_logs_no_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER operation_logs_no_update BEFORE UPDATE ON public.operation_logs FOR EACH ROW WHEN (((new.actor_user_id IS NOT NULL) OR (old.actor_user_id IS NULL))) EXECUTE FUNCTION public.operation_logs_append_only();


--
-- Name: attachments attachments_post_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.attachments
    ADD CONSTRAINT attachments_post_id_fkey FOREIGN KEY (post_id) REFERENCES public.posts(id) ON DELETE CASCADE;


--
-- Name: board_fields board_fields_board_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.board_fields
    ADD CONSTRAINT board_fields_board_id_fkey FOREIGN KEY (board_id) REFERENCES public.boards(id) ON DELETE CASCADE;


--
-- Name: cart_items cart_items_cart_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_items
    ADD CONSTRAINT cart_items_cart_id_fkey FOREIGN KEY (cart_id) REFERENCES public.carts(id) ON DELETE CASCADE;


--
-- Name: cart_items cart_items_variant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_items
    ADD CONSTRAINT cart_items_variant_id_fkey FOREIGN KEY (variant_id) REFERENCES public.product_variants(id) ON DELETE CASCADE;


--
-- Name: carts carts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.carts
    ADD CONSTRAINT carts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: categories categories_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.categories
    ADD CONSTRAINT categories_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.categories(id) ON DELETE RESTRICT;


--
-- Name: comments comments_author_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT comments_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: comments comments_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT comments_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.comments(id);


--
-- Name: comments comments_post_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.comments
    ADD CONSTRAINT comments_post_id_fkey FOREIGN KEY (post_id) REFERENCES public.posts(id) ON DELETE CASCADE;


--
-- Name: email_verification_tokens email_verification_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_verification_tokens
    ADD CONSTRAINT email_verification_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: menus menus_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.menus
    ADD CONSTRAINT menus_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.menus(id) ON DELETE CASCADE;


--
-- Name: operation_logs operation_logs_actor_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operation_logs
    ADD CONSTRAINT operation_logs_actor_user_id_fkey FOREIGN KEY (actor_user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: order_agreements order_agreements_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_agreements
    ADD CONSTRAINT order_agreements_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- Name: order_agreements order_agreements_terms_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_agreements
    ADD CONSTRAINT order_agreements_terms_id_fkey FOREIGN KEY (terms_id) REFERENCES public.terms(id) ON DELETE RESTRICT;


--
-- Name: order_items order_items_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- Name: order_items order_items_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE RESTRICT;


--
-- Name: order_items order_items_variant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.order_items
    ADD CONSTRAINT order_items_variant_id_fkey FOREIGN KEY (variant_id) REFERENCES public.product_variants(id) ON DELETE RESTRICT;


--
-- Name: orders orders_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE RESTRICT;


--
-- Name: password_reset_tokens password_reset_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.password_reset_tokens
    ADD CONSTRAINT password_reset_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: payments payments_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- Name: payments payments_return_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_return_fk FOREIGN KEY (return_id) REFERENCES public.returns(id) ON DELETE RESTRICT;


--
-- Name: posts posts_author_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.posts
    ADD CONSTRAINT posts_author_id_fkey FOREIGN KEY (author_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: posts posts_board_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.posts
    ADD CONSTRAINT posts_board_id_fkey FOREIGN KEY (board_id) REFERENCES public.boards(id) ON DELETE CASCADE;


--
-- Name: product_categories product_categories_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_categories
    ADD CONSTRAINT product_categories_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.categories(id) ON DELETE RESTRICT;


--
-- Name: product_categories product_categories_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_categories
    ADD CONSTRAINT product_categories_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE CASCADE;


--
-- Name: product_options product_options_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_options
    ADD CONSTRAINT product_options_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE CASCADE;


--
-- Name: product_variants product_variants_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.product_variants
    ADD CONSTRAINT product_variants_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id) ON DELETE CASCADE;


--
-- Name: refund_items refund_items_order_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refund_items
    ADD CONSTRAINT refund_items_order_item_id_fkey FOREIGN KEY (order_item_id) REFERENCES public.order_items(id) ON DELETE RESTRICT;


--
-- Name: refund_items refund_items_refund_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refund_items
    ADD CONSTRAINT refund_items_refund_id_fkey FOREIGN KEY (refund_id) REFERENCES public.refunds(id) ON DELETE CASCADE;


--
-- Name: refunds refunds_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refunds
    ADD CONSTRAINT refunds_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- Name: refunds refunds_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refunds
    ADD CONSTRAINT refunds_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: refunds refunds_return_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.refunds
    ADD CONSTRAINT refunds_return_fk FOREIGN KEY (return_id) REFERENCES public.returns(id) ON DELETE RESTRICT;


--
-- Name: return_items return_items_order_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.return_items
    ADD CONSTRAINT return_items_order_item_id_fkey FOREIGN KEY (order_item_id) REFERENCES public.order_items(id) ON DELETE RESTRICT;


--
-- Name: return_items return_items_return_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.return_items
    ADD CONSTRAINT return_items_return_id_fkey FOREIGN KEY (return_id) REFERENCES public.returns(id) ON DELETE CASCADE;


--
-- Name: returns returns_new_variant_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.returns
    ADD CONSTRAINT returns_new_variant_id_fkey FOREIGN KEY (new_variant_id) REFERENCES public.product_variants(id) ON DELETE RESTRICT;


--
-- Name: returns returns_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.returns
    ADD CONSTRAINT returns_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- Name: role_permissions role_permissions_board_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_board_id_fkey FOREIGN KEY (board_id) REFERENCES public.boards(id) ON DELETE CASCADE;


--
-- Name: role_permissions role_permissions_permission_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_permission_id_fkey FOREIGN KEY (permission_id) REFERENCES public.permissions(id) ON DELETE RESTRICT;


--
-- Name: role_permissions role_permissions_role_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE CASCADE;


--
-- Name: shipments shipments_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.shipments
    ADD CONSTRAINT shipments_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- Name: shipments shipments_return_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.shipments
    ADD CONSTRAINT shipments_return_id_fkey FOREIGN KEY (return_id) REFERENCES public.returns(id) ON DELETE RESTRICT;


--
-- Name: social_accounts social_accounts_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.social_accounts
    ADD CONSTRAINT social_accounts_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_roles user_roles_role_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_roles
    ADD CONSTRAINT user_roles_role_id_fkey FOREIGN KEY (role_id) REFERENCES public.roles(id) ON DELETE RESTRICT;


--
-- Name: user_roles user_roles_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_roles
    ADD CONSTRAINT user_roles_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: webhook_events webhook_events_order_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_events
    ADD CONSTRAINT webhook_events_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;


--
-- PostgreSQL database dump complete
--

\unrestrict hvLJ9HjDe2rmfPDCNPMG7xMADVSVlUaHZuGNxtAaKnw1UlFGhypp29nfXcwc0H7

