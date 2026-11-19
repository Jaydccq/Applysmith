% Document class of the cover letter, used by cover.tex under XeLaTeX.
%
% The page is one column with equal side margins of 2.8 cm, no running
% header or footer, and a bottom margin that keeps body text out of the band
% a page number would use. The name at the top is set in Lato Light; every
% other letter is Raleway Medium at 11 pt on 14 pt. Both fonts are read from
% the fonts directory copied beside the source, never from the system.
% Raleway is loaded with kerning off, because a text extractor reads a kerned
% pair as a word break, and with lining figures, because its default
% figures are old style. Bold, italic, sans and mono all map to the same
% Raleway file (bold and italic synthesised), so no markup can fall back to
% another font.
%
% The letter is written with the commands below, in page order:
%   \lettername, \contactitem/\contactlink, \contactbreak, \linkitem,
%   \headerclose, \letterdate, \recipientline, \salutation, \bodyparagraph, the bodylist
%   environment, \closing and \signature.
\NeedsTeXFormat{LaTeX2e}
\ProvidesClass{cover}
\newcommand*{\cover@paper}{letterpaper}
\DeclareOption{a4paper}{\renewcommand*{\cover@paper}{a4paper}}
\DeclareOption{letterpaper}{\renewcommand*{\cover@paper}{letterpaper}}
\DeclareOption*{\ClassError{cover}{Unknown option \CurrentOption}{}}
\ProcessOptions\relax
\LoadClass{article}

\RequirePackage[\cover@paper,hmargin=2.8cm,top=2cm,bottom=3.4cm]{geometry}
\RequirePackage{fontspec}
\RequirePackage[hidelinks]{hyperref}
\RequirePackage{xcolor}
% The résumé's accent colour, for the name, the rule under the header and
% the list markers; the contact lines are dark grey, the body black.
\definecolor{accent}{RGB}{28,61,105}
\definecolor{contactgrey}{gray}{0.35}

\newcommand*{\cover@bodyfeatures}{Path=fonts/raleway/,Kerning=Off,Numbers=Lining,
  BoldFont=Raleway-Medium.otf,BoldFeatures={FakeBold=2},
  ItalicFont=Raleway-Medium.otf,ItalicFeatures={FakeSlant=0.18},
  BoldItalicFont=Raleway-Medium.otf,BoldItalicFeatures={FakeBold=2,FakeSlant=0.18}}
\edef\cover@load{\noexpand\setmainfont{Raleway-Medium.otf}[\cover@bodyfeatures]}\cover@load
\edef\cover@load{\noexpand\setsansfont{Raleway-Medium.otf}[\cover@bodyfeatures]}\cover@load
\edef\cover@load{\noexpand\setmonofont{Raleway-Medium.otf}[\cover@bodyfeatures]}\cover@load
\newfontfamily\cover@namefont{Lato-Light.ttf}[Path=fonts/lato/]

% One body size everywhere: the class has no smaller or larger body text.
\renewcommand*{\normalsize}{\fontsize{11pt}{14pt}\selectfont}
\normalsize
\setlength{\parindent}{0pt}
\setlength{\parskip}{0pt}
\raggedright
\pagestyle{empty}

% The gap between paragraphs; a list has the same gap above and below it.
\newlength{\blockgap}
\setlength{\blockgap}{9pt}
\newcommand*{\cover@block}{\par\addvspace{\blockgap}}

\newcommand*{\lettername}[1]{%
  {\cover@namefont\fontsize{28pt}{32pt}\selectfont\color{accent}#1\par}%
  \addvspace{8pt}\color{contactgrey}}

% The contact line: items joined by a bar that disappears where the line
% wraps, so no line starts or ends with a separator (the \unskip removes the
% space the source's line end leaves before an item). \contactbreak ends the line;
% \linkitem items then fill the line below it in the same way.
\newif\ifcover@open
\newcommand*{\cover@item}[1]{%
  \ifcover@open\unskip\discretionary{}{}{\kern.5em\textbar\kern.5em}\fi
  \cover@opentrue\mbox{#1}}
\newcommand*{\contactitem}[1]{\cover@item{#1}}
\newcommand*{\contactlink}[2]{\cover@item{\href{#1}{#2}}}
\newcommand*{\contactbreak}{\par\cover@openfalse}
\newcommand*{\linkitem}[2]{\cover@item{\href{#1}{#2}}}

% \headerclose closes the header with a thin rule in the accent colour and
% returns to black; the date, when there is one, opens the body below it.
\newcommand*{\headerclose}{\par\cover@openfalse\vspace{8pt}%
  {\color{accent}\hrule height .6pt}\color{black}\vspace{2\blockgap}}
\newcommand*{\letterdate}[1]{#1\par}
\newif\ifcover@inaddress
\newcommand*{\recipientline}[1]{%
  \ifcover@inaddress\else\cover@block\cover@inaddresstrue\fi#1\par}
\newcommand*{\salutation}[1]{\cover@block#1\par}
\newcommand*{\bodyparagraph}[1]{\cover@block#1\par}
\newenvironment{bodylist}
  {\cover@block\begin{list}{\textcolor{accent}{\textbullet}}{%
     \setlength{\leftmargin}{1.2em}\setlength{\labelsep}{.5em}%
     \setlength{\labelwidth}{.7em}\setlength{\topsep}{0pt}%
     \setlength{\partopsep}{0pt}\setlength{\parsep}{0pt}%
     \setlength{\itemsep}{4pt}}}
  {\end{list}\cover@block}
\newcommand*{\closing}[1]{\cover@block#1\par}
% Two empty body lines above the name leave room for a signature.
\newcommand*{\signature}[1]{\vspace{26pt}#1\par}
