// Wire types mirroring the Go API contract (api/openapi.yaml). Field names match the JSON exactly.
export interface User {
  id: number;
  username: string;
  email: string;
  role: string; // "Admin" | "Editor" | "Author" | "Subscriber"
  role_id: number; // 1=Admin, 2=Editor, 3=Author, 4=Subscriber
}

export interface AuthResponse {
  access_token: string;
  user: User;
}

// Standard paginated list wrapper ({items, page, per_page, total}).
export interface Page<T> {
  items: T[];
  page: number;
  per_page: number;
  total: number;
}

export interface Post {
  id: number;
  title: string;
  slug: string;
  code?: string; // hashid
  author_name: string;
  category_name?: string;
  status: string;
  created_at: string;
  featured_image_path?: string;
  excerpt: string;
  markdown_content?: string;
  html_content?: string,
  tags?: Record<string, string>
}

// Error envelope: {error, message, fields?}. `fields` is present only on 400 invalid_input validation failures.
export interface ApiErrorBody {
  error: string;
  message?: string;
  fields?: Record<string, string>;
}
