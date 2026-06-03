import { Link } from "react-router-dom";

export function NotFound() {
  return (
    <section>
      <h1>404</h1>
      <p>That page doesn’t exist.</p>
      <Link to="/">Back home</Link>
    </section>
  );
}
