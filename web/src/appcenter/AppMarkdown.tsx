import Markdown, { type Components } from "react-markdown";
import remarkBreaks from "remark-breaks";
import remarkGfm from "remark-gfm";

// Manifest text is Markdown with GitHub tables, written with single line
// breaks meant as breaks. Raw HTML is never rendered, images are dropped
// (they would load from the internet), and links open in a new window. The
// text sits under the page's own section headings, so its headings start
// one level below them.
const components: Components = {
  a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer" />,
  h1: ({ node: _node, ...props }) => <h4 {...props} />,
  h2: ({ node: _node, ...props }) => <h4 {...props} />,
  h3: ({ node: _node, ...props }) => <h4 {...props} />,
  h4: ({ node: _node, ...props }) => <h5 {...props} />,
  h5: ({ node: _node, ...props }) => <h5 {...props} />,
  h6: ({ node: _node, ...props }) => <h5 {...props} />,
};

export default function AppMarkdown({ children }: { children: string }) {
  return (
    <Markdown
      remarkPlugins={[remarkGfm, remarkBreaks]}
      disallowedElements={["img"]}
      unwrapDisallowed
      components={components}
    >
      {children}
    </Markdown>
  );
}
