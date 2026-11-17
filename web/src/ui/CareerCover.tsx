import { ArrowRight, Compass, MessageSquareText } from "lucide-react";
import { Link } from "../router";
import cover from "../assets/career-cover.webp";

export function CareerCover() {
  return (
    <section className="career-cover" aria-labelledby="career-cover-title">
      <img className="career-cover-art" src={cover} alt="" width="1983" height="793" fetchPriority="high" />
      <div className="career-cover-copy">
        <p className="career-eyebrow">YOUR CAREER COMPANION</p>
        <h2 id="career-cover-title">Your next chapter<br />starts here.</h2>
        <p className="career-cover-description">A little help finding your people, preparing your applications, and taking the next step.</p>
        <div className="career-cover-actions">
          <Link to="/discovery" className="button primary"><Compass />Find my next role<ArrowRight /></Link>
          <Link to="/ask" className="career-chat-link"><MessageSquareText />Let&apos;s make a plan</Link>
        </div>
      </div>
    </section>
  );
}
