// Home is the landing page: it links to the public (anonymous) API endpoints and
// also doubles as an API connectivity smoke test by listing published post titles
// from GET /posts. An empty list (fresh DB) is fine — it still proves the SPA is
// served by Go and can reach the API.

import { useEffect, useState } from "react";
import { listPosts } from "../lib/api";
import type { Post } from "../types";

// Directly GET-able public endpoints (rendered as links).
const PUBLIC_LINKS: { href: string; label: string }[] = [
  { href: "/posts", label: "Published posts (JSON)" },
  { href: "/categories", label: "Categories (JSON)" },
  { href: "/tags", label: "Tags (JSON)" },
  { href: "/docs", label: "Swagger UI — interactive API docs" },
  { href: "/openapi.json", label: "OpenAPI spec (JSON)" },
  { href: "/openapi.yaml", label: "OpenAPI spec (YAML)" },
];

// Public endpoints that take a path parameter (shown as patterns, not links).
const PUBLIC_PATTERNS: string[] = [
  "GET /posts/{slug} — read one post by slug",
  "GET /p/{code} — read one post by hashid",
  "GET /categories/{slug} — posts in a category",
  "GET /tags/{slug} — posts with a tag",
  "GET /s/{code} — resolve a short link",
];

export function Home() {
  const [posts, setPosts] = useState<Post[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const path = "./uploads/"

  useEffect(() => {
    let cancelled = false;
    listPosts()
      .then((page) => {
        if (!cancelled) setPosts(page.items);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "failed to load posts");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <section>
      <h1>micro-blog</h1>
      {/*<p>Markdown micro-blog API with a built-in URL shortener. Public endpoints:</p>*/}

      {/*<h2>Explore</h2>*/}
      {/*<ul>*/}
      {/*  {PUBLIC_LINKS.map((l) => (*/}
      {/*    <li key={l.href}>*/}
      {/*      <a href={l.href}>{l.href}</a> — {l.label}*/}
      {/*    </li>*/}
      {/*  ))}*/}
      {/*</ul>*/}

      {/*<h2>Parameterized</h2>*/}
      {/*<ul>*/}
      {/*  {PUBLIC_PATTERNS.map((p) => (*/}
      {/*    <li key={p}>*/}
      {/*      <code>{p}</code>*/}
      {/*    </li>*/}
      {/*  ))}*/}
      {/*</ul>*/}

      <h2>Latest posts</h2>
      {loading && <p>Loading…</p>}
      {error && <p className="error">Could not load posts: {error}</p>}
      {!loading && !error && posts.length === 0 && <p>No published posts yet.</p>}
      <ul>
        {posts.map((p) => (
            <a href={p.slug}>
                <li key={p.id}>
                    {p.featured_image_path ? <img src={path + p.featured_image_path} alt={p.featured_image_path}/> : ''}
                    {p.title} <small>— {p.author_name}</small>
                  <p>{p.excerpt}</p>
                </li>
            </a>
        ))}
      </ul>
    </section>
  );
}
