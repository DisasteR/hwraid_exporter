# Base légale : exception d'interopérabilité

Ce document explique sur quels textes repose la publication, dans ce dépôt, d'informations établies par l'analyse de l'outil constructeur Adaptec / Microchip (ARCCONF). Il est fourni pour la transparence du projet. **Ce n'est pas un avis juridique** : il doit être relu par un juriste avant que le dépôt soit rendu public.

## 1. Ce qui est publié, et ce qui ne l'est pas

| Publié | Non publié |
|---|---|
| Le code de l'exporter, qui envoie des commandes au pilote Linux `aacraid` et décode les réponses | La méthode d'analyse de l'outil constructeur |
| La description des requêtes et des formats de réponse que l'exporter utilise ([aacraid-interface.md](aacraid-interface.md)), avec la provenance de chaque élément | Les adresses, le désassemblage, les noms de fonctions et les énumérations internes de l'outil |
| Les règles d'interprétation nécessaires pour calculer un état (membre, volume, disque) | Les captures brutes (elles contiennent aussi des numéros de série et des WWN) |

L'exporter n'est pas un substitut d'ARCCONF : il ne fait que lire l'état et ne modifie aucune configuration. Il ne contient aucun code de l'outil constructeur et ne le redistribue pas.

Trois provenances sont distinguées dans [aacraid-interface.md](aacraid-interface.md) :

- **[public]** : sources publiques (pilote Linux `aacraid` sous GPL, pilote FreeBSD `aacraid`) ;
- **[observé]** : observation du fonctionnement de l'outil pendant son exécution normale (requêtes envoyées au pilote et réponses), sans décompilation ;
- **[interopérabilité]** : informations obtenues par décompilation de l'outil, faute de source publique.

Les sections 2 à 5 donnent les textes qui couvrent chacune. La section 6 les applique au projet. La section 7 liste les risques non couverts.

## 2. Union européenne

### Directive 2009/24/CE (protection juridique des programmes d'ordinateur)

Texte officiel : <https://eur-lex.europa.eu/eli/dir/2009/24/oj>. Relu dans sa version anglaise republiée : <https://www.legislation.gov.uk/eudr/2009/24/contents>.

| Disposition | Contenu | Portée pour le projet |
|---|---|---|
| Art. 1 §2 | Les idées et principes à la base d'un élément d'un programme, y compris ses interfaces, ne sont pas protégés par le droit d'auteur | Un format de réponse ou une règle d'état sont des idées et principes, pas l'expression du programme |
| Art. 5 §3 | La personne qui a le droit d'utiliser le programme peut, sans autorisation, en observer, étudier ou tester le fonctionnement pour déterminer ces idées et principes, pendant les opérations qu'elle a le droit d'effectuer | Couvre la provenance [observé] |
| Art. 6 §1 | La reproduction du code et la traduction de sa forme ne demandent pas d'autorisation quand elles sont indispensables pour obtenir les informations nécessaires à l'interopérabilité d'un programme créé de façon indépendante. Trois conditions cumulatives : a) actes faits par le licencié ou une personne ayant le droit d'utiliser une copie ; b) informations pas déjà facilement et rapidement accessibles ; c) actes limités aux parties nécessaires | Couvre la provenance [interopérabilité] |
| Art. 6 §2 | Les informations obtenues ne peuvent pas : a) servir à d'autres fins que l'interopérabilité ; b) être communiquées à des tiers, **sauf si cela est nécessaire à l'interopérabilité** du programme créé de façon indépendante ; c) servir à un programme substantiellement similaire ou à tout autre acte qui porte atteinte au droit d'auteur | Fonde et borne la publication |
| Art. 6 §3 | L'article 6 ne peut pas être interprété de façon à causer un préjudice injustifié aux intérêts légitimes du titulaire ou à porter atteinte à l'exploitation normale du programme | Limite générale (test en trois étapes) |
| Art. 8 | Toute disposition contractuelle contraire à l'article 6 ou aux exceptions de l'article 5 §2 et §3 est nulle et non avenue. La directive n'affecte pas les autres régimes (brevets, marques, concurrence déloyale, secrets d'affaires, droit des contrats) | Rend inopposable l'interdiction de la licence, sans écarter les autres régimes |
| Considérant 10 | Interopérabilité : capacité d'échanger des informations et d'utiliser mutuellement les informations échangées | Un exporter qui lit l'état d'une carte répond à cette définition |
| Considérants 15 et 16 | La décompilation est légitime dans ces circonstances limitées ; les clauses contractuelles contraires sont nulles | Interprétation |

### Jurisprudence de la Cour de justice de l'Union européenne

- **CJUE, 2 mai 2012, SAS Institute, C-406/10**, ECLI:EU:C:2012:259 (<https://curia.europa.eu/juris/liste.jsf?num=C-406/10>). Ni la fonctionnalité d'un programme, ni son langage de programmation, ni le format des fichiers de données ne sont protégés en tant que tels. Le licencié peut observer, étudier et tester le programme sans accès au code source, et une clause qui l'interdit est nulle.
- **CJUE, 6 octobre 2021, Top System, C-13/20**, ECLI:EU:C:2021:811 (<https://curia.europa.eu/juris/liste.jsf?num=C-13/20>). La décompilation peut aussi être permise au titre de l'article 5 §1, pour corriger des erreurs. Les conditions de l'article 6 ne s'appliquent pas alors. Cet arrêt n'est pas le fondement du projet, mais il confirme que la décompilation n'est pas interdite par principe.

### Directive (UE) 2016/943 (secrets d'affaires)

Texte officiel : <https://eur-lex.europa.eu/eli/dir/2016/943/oj>. Relu dans sa version anglaise : <https://www.legislation.gov.uk/eudr/2016/943/article/3>.

- **Art. 3 §1 b)** : l'obtention d'un secret d'affaires est licite lorsqu'elle résulte de l'observation, de l'étude, du démontage ou du test d'un produit mis à la disposition du public ou détenu de façon licite, **par une personne qui n'est liée par aucune obligation juridiquement valide de limiter cette obtention**.
- **Art. 3 §2** : l'obtention, l'utilisation et la divulgation sont licites dans la mesure où le droit de l'Union ou le droit national l'impose ou le permet.

La licence d'ARCCONF déclare que l'outil contient des secrets d'affaires et interdit la rétro-ingénierie. Une telle clause pourrait être une « obligation juridiquement valide » au sens de l'art. 3 §1 b), **sauf** pour les actes que la directive 2009/24 rend licites malgré le contrat (art. 8). L'art. 3 §2 renvoie alors à ces textes. Cette articulation n'a pas encore été tranchée par la Cour de justice [Probable, non vérifié en jurisprudence].

### Loi applicable

La licence d'ARCCONF (§12) choisit le droit californien, sauf pour le droit d'auteur, soumis au droit fédéral américain. Deux règles européennes limitent la portée de ce choix pour un utilisateur établi dans l'Union :

- **Règlement (CE) 864/2007 (Rome II), art. 8** : la loi applicable à une atteinte à un droit de propriété intellectuelle est celle du pays pour lequel la protection est revendiquée. Pour des actes faits en France, les exceptions du droit d'auteur français s'appliquent quel que soit le contrat. Cette règle ne peut pas être écartée par un choix contractuel (art. 8 §3).
- **Règlement (CE) 593/2008 (Rome I), art. 6 §2 et art. 9** : le choix de loi ne peut pas priver un consommateur de la protection des dispositions impératives de son pays de résidence. Les lois de police du for restent applicables. La qualification de l'art. L122-6-1 du CPI comme loi de police internationale n'est pas établie [Supposition].

Textes : <https://eur-lex.europa.eu/eli/reg/2007/864/oj> et <https://eur-lex.europa.eu/eli/reg/2008/593/oj> (non relus dans le cadre de ce document).

## 3. France

### Code de la propriété intellectuelle, article L122-6-1

Version en vigueur depuis le 26 novembre 2021 (ordonnance n° 2021-1518) : <https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000006278920>.

- **III** : la personne ayant le droit d'utiliser le logiciel peut, sans l'autorisation de l'auteur, observer, étudier ou tester son fonctionnement ou sa sécurité, afin de déterminer les idées et principes qui sont à la base de n'importe quel élément du logiciel.
- **IV** : la reproduction du code ou la traduction de sa forme n'est pas soumise à autorisation lorsqu'elle est indispensable pour obtenir les informations nécessaires à l'interopérabilité d'un logiciel créé de façon indépendante. Les conditions 1° à 3° reprennent l'article 6 §1 de la directive. Les informations obtenues ne peuvent être « ni communiquées à des tiers sauf si cela est nécessaire à l'interopérabilité du logiciel créé de façon indépendante ».
- **Dernier alinéa** : « Toute stipulation contraire aux dispositions prévues aux II, III et IV du présent article est nulle et non avenue. »

### Code de commerce, article L151-3

Transposition de l'art. 3 de la directive 2016/943 (obtention licite d'un secret des affaires par observation, étude, démontage ou test). Texte non relu dans le cadre de ce document : à vérifier sur Légifrance.

## 4. États-Unis

La licence d'ARCCONF désigne le droit californien et le droit fédéral américain. Ces textes comptent pour une action intentée aux États-Unis, et parce que le dépôt est hébergé par un service américain.

| Texte | Contenu | Source |
|---|---|---|
| 17 U.S.C. § 102(b) | Le droit d'auteur ne protège jamais une idée, une procédure, un procédé, un système ou une méthode de fonctionnement | <https://www.law.cornell.edu/uscode/text/17/102> |
| 17 U.S.C. § 1201(f) | Exception d'interopérabilité à l'interdiction de contourner une mesure technique de protection. Le (3) permet de communiquer les informations obtenues à d'autres « solely for the purpose of enabling interoperability ». Le (4) définit l'interopérabilité | <https://www.law.cornell.edu/uscode/text/17/1201> |
| 18 U.S.C. § 1839(6)(B) (Defend Trade Secrets Act) | Les moyens illicites d'obtention d'un secret d'affaires n'incluent pas la rétro-ingénierie | <https://www.law.cornell.edu/uscode/text/18/1839> |
| Cal. Civ. Code § 3426.1(a) (Uniform Trade Secrets Act californien) | La rétro-ingénierie ou la dérivation indépendante seule n'est pas un moyen illicite | Relu via une source secondaire : <https://codes.findlaw.com/ca/civil-code/civ-sect-3426-1/> ; texte officiel : <https://leginfo.legislature.ca.gov/faces/codes_displaySection.xhtml?lawCode=CIV&sectionNum=3426.1> |

Le § 1201(f) ne vise que le contournement d'une mesure technique de protection. Aucune mesure de ce type n'a été contournée pour ce projet [Probable : l'outil a été analysé tel qu'il est distribué].

### Jurisprudence

- **Sega Enterprises v. Accolade, 977 F.2d 1510 (9th Cir. 1992)** : désassembler un programme pour accéder à ses éléments fonctionnels non protégés, afin de produire un programme compatible, relève du *fair use*. Résumé du Copyright Office : <https://www.copyright.gov/fair-use/summaries/segaenters-accolade-9thcir1992.pdf>.
- **Sony Computer Entertainment v. Connectix, 203 F.3d 596 (9th Cir. 2000)** : les copies intermédiaires faites pendant la rétro-ingénierie, pour réaliser un produit non contrefaisant et compatible, relèvent du *fair use*. Résumé : <https://www.copyright.gov/fair-use/summaries/sony-connectix-9thcir2000.pdf>.
- **Contrepoint, Bowers v. Baystate Technologies, 320 F.3d 1317 (Fed. Cir. 2003)** : une clause de licence qui interdit la rétro-ingénierie est valable et n'est pas écartée par le droit d'auteur fédéral. L'utilisateur qui la viole engage sa responsabilité contractuelle, même si l'acte serait permis par le droit d'auteur. Texte : <https://caselaw.findlaw.com/court/us-federal-circuit/1202776.html>.
- **Aqua Connect v. Code Rebel (C.D. Cal.)** : une clause de licence interdisant la rétro-ingénierie ne transforme pas celle-ci en moyen illicite au sens du droit californien des secrets d'affaires ; elle peut en revanche fonder une action contractuelle. Décision de première instance, non contraignante. Analyse : <https://www.littler.com/publication-press/publication/california-court-finds-obtaining-trade-secrets-through-reverse>.

**En droit américain, le risque principal est contractuel, pas lié au droit d'auteur ni au secret d'affaires.** L'interdiction de la licence n'y est pas nulle comme dans l'Union européenne.

## 5. Autres juridictions

| Pays | Texte | Contenu | Statut |
|---|---|---|---|
| Royaume-Uni | Copyright, Designs and Patents Act 1988, s.50B | Décompilation permise pour créer un programme indépendant interopérable. Les informations ne doivent pas être communiquées à quelqu'un qui n'en a pas besoin pour cet objectif. Une clause contractuelle contraire est sans effet (4) | Relu : <https://www.legislation.gov.uk/ukpga/1988/48/section/50B> |
| Royaume-Uni | CDPA 1988, s.296A | Est nulle toute clause qui interdit ou restreint la décompilation dans les conditions de s.50B(2), ou l'observation et le test selon s.50BA | Relu : <https://www.legislation.gov.uk/ukpga/1988/48/section/296A> |
| Allemagne | UrhG § 69e | Décompilation pour l'interopérabilité ; communication à des tiers seulement si nécessaire à l'interopérabilité | Relu : <https://www.gesetze-im-internet.de/urhg/__69e.html> |
| Allemagne | UrhG § 69g (2) | Nullité des clauses contraires à § 69d (2), (3), (5), (7) et à § 69e | Relu : <https://www.gesetze-im-internet.de/urhg/__69g.html> |
| Canada | Loi sur le droit d'auteur, art. 30.61 et 41.12 | Adaptation pour l'interopérabilité ; contournement d'une mesure technique pour l'interopérabilité | Non relu |
| Australie | Copyright Act 1968, s.47D | Reproduction pour l'interopérabilité | Non relu |
| Suisse | Loi sur le droit d'auteur, art. 21 | Décompilation pour l'interopérabilité | Non relu |

Tous les États membres de l'Union appliquent l'article 6 de la directive 2009/24 dans leur droit national.

## 6. Application au projet

| Condition (directive 2009/24, art. 6) | Situation du projet | Appréciation |
|---|---|---|
| §1 a) Actes faits par une personne ayant le droit d'utiliser une copie | L'analyse a été faite par le propriétaire de la carte, sur l'outil fourni par le constructeur pour cette carte | Remplie [Probable : suppose une licence acceptée régulièrement] |
| §1 b) Informations pas déjà facilement accessibles | Les sources publiques (pilotes Linux et FreeBSD) sont utilisées d'abord. L'analyse ne porte que sur ce qu'elles ne donnent pas : état des membres, règle de calcul de l'état, états des disques | Remplie pour les sources consultées. Reste à vérifier : le constructeur publie-t-il une interface de gestion documentée (SDK, API) qui donnerait ces informations ? |
| §1 c) Actes limités aux parties nécessaires | Analyse ciblée sur les lectures utilisées par l'exporter | Remplie [Probable] |
| §2 a) Pas d'autre fin que l'interopérabilité | Les informations servent seulement à décoder l'état | Remplie |
| §2 b) Communication à des tiers seulement si nécessaire | Code de l'exporter : nécessaire pour que tout utilisateur fasse fonctionner le programme. Documentation des formats : utile pour maintenir, vérifier et porter l'exporter sur d'autres modèles | Code : remplie. Documentation : **c'est le point le plus discutable** (section 7) |
| §2 c) Pas de programme substantiellement similaire | L'exporter ne gère pas la carte, ne reprend aucune interface ni aucun code de l'outil | Remplie |
| §3 Pas de préjudice injustifié | L'outil constructeur reste nécessaire pour gérer la carte ; l'exporter ne fait que lire | Remplie [Probable] |

Les clauses de la licence d'ARCCONF :

- **§2 (interdiction de la rétro-ingénierie)** : nulle pour les actes couverts par l'art. 5 §3 et l'art. 6 (directive, art. 8 ; CPI, dernier alinéa de L122-6-1 ; CDPA s.296A ; UrhG § 69g).
- **§4 (confidentialité)** : ne peut pas empêcher une communication que l'art. 6 §2 b) permet, puisque l'art. 8 annule toute clause contraire à l'article 6 [Probable]. Elle reste applicable au reste : la méthode et les éléments internes ne sont pas publiés.
- **§12 (droit californien)** : voir les sections 2 (loi applicable) et 7.

## 7. Risques identifiés

1. **Documentation publique des formats.** Le code qui applique les informations est clairement « nécessaire à l'interopérabilité ». Une documentation explicative va plus loin. Elle se justifie par la maintenance et la vérification de l'exporter. Une lecture stricte de l'art. 6 §2 b) pourrait toutefois ne couvrir que le code. Pour réduire ce risque, la documentation se limite à ce que fait l'exporter, sans rien décrire de l'outil lui-même.
2. **Action contractuelle aux États-Unis.** Selon *Bowers v. Baystate*, une juridiction américaine pourrait appliquer l'interdiction de la licence. La protection européenne repose sur la nullité de la clause dans l'Union. Elle ne garantit pas l'issue d'une action intentée aux États-Unis.
3. **Hébergement du dépôt.** Un hébergeur américain peut recevoir une demande de retrait. Le dépôt ne contient aucun code de l'outil ; une demande fondée sur le droit d'auteur serait donc mal fondée, mais elle reste possible.
4. **Informations déjà accessibles.** Si le constructeur publie une interface documentée, la condition §1 b) tombe pour les informations qu'elle couvre. Il faut alors utiliser cette source.

## 8. Avant de rendre le dépôt public

- Faire relire ce document par un juriste en propriété intellectuelle.
- Vérifier sur EUR-Lex et Légifrance les versions françaises officielles des textes cités, ainsi que les textes marqués « non relu ».
- Vérifier qu'aucune interface de gestion documentée n'est publiée par le constructeur pour cette famille de cartes.
- Lancer `scripts/check-public.sh` : il bloque les éléments internes de l'analyse.
