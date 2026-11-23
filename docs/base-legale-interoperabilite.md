# Base légale : exception d'interopérabilité

Ce document explique sur quels textes repose la publication, dans ce dépôt, d'informations établies par l'analyse de l'outil de gestion Adaptec / Microchip (ARCCONF), et dans quelles limites. Chaque texte cité a été relu dans sa version officielle, sauf mention contraire ; l'URL lue est indiquée.

## 1. Ce qui est publié, et ce qui ne l'est pas

| Publié | Non publié |
|---|---|
| Le code de l'exporter, qui envoie des commandes au pilote Linux `aacraid` et décode les réponses | La méthode d'analyse de l'outil constructeur |
| La description des requêtes et des formats de réponse que l'exporter utilise ([aacraid-interface.md](aacraid-interface.md)), avec la provenance de chaque élément | Les adresses, le désassemblage, les noms de fonctions et les énumérations internes de l'outil |
| Les règles d'interprétation nécessaires pour calculer un état (membre, volume, disque) et le critère qui dit quelles cartes utilisent ces lectures | Les captures brutes, qui contiennent aussi des numéros de série et des WWN |
| L'outil de capture [`fibcap`](../tools/fibcap) et le [guide de capture](guide-capture-aacraid.md) | — |

L'exporter n'est pas un substitut d'ARCCONF : il ne fait que lire l'état et ne modifie aucune configuration. Il ne contient aucun code de l'outil constructeur et ne le redistribue pas.

[aacraid-interface.md](aacraid-interface.md) distingue trois provenances :

- **[public]** : sources publiques (pilotes Linux et FreeBSD `aacraid`, sous licence libre) ;
- **[observé]** : observation du fonctionnement de l'outil pendant son exécution normale (requêtes envoyées au pilote et réponses), sans décompilation ;
- **[interopérabilité]** : informations obtenues par décompilation de l'outil, faute de source publique.

## 2. Union européenne

### Directive 2009/24/CE (protection juridique des programmes d'ordinateur)

Lue sur <https://eur-lex.europa.eu/legal-content/FR/TXT/?uri=CELEX:32009L0024>.

| Disposition | Contenu | Portée pour le projet |
|---|---|---|
| Art. 1 §2 | « Les idées et principes qui sont à la base de quelque élément que ce soit d'un programme d'ordinateur, y compris ceux qui sont à la base de ses interfaces, ne sont pas protégés par le droit d'auteur » | Un format de réponse ou une règle d'état sont des idées et principes, pas l'expression du programme |
| Art. 5 §3 | La personne habilitée à utiliser une copie peut, sans autorisation, « observer, étudier ou tester le fonctionnement de ce programme » pour déterminer ces idées et principes, pendant les opérations de chargement, d'affichage, d'exécution, de transmission ou de stockage qu'elle est en droit d'effectuer | Couvre la provenance [observé] et le guide de capture. Pas de condition d'information déjà accessible |
| Art. 6 §1 | La reproduction du code et la traduction de sa forme ne demandent pas d'autorisation lorsqu'elles sont « indispensable[s] pour obtenir les informations nécessaires à l'interopérabilité d'un programme d'ordinateur créé de façon indépendante ». Conditions : a) actes du licencié ou d'une personne ayant le droit d'utiliser une copie ; b) informations « pas déjà […] facilement et rapidement accessibles » ; c) actes limités aux parties nécessaires | Couvre la provenance [interopérabilité] |
| Art. 6 §2 | Les informations obtenues ne peuvent pas : a) servir à d'autres fins que l'interopérabilité ; b) être « communiquées à des tiers, sauf si cela s'avère nécessaire à l'interopérabilité du programme d'ordinateur créé de façon indépendante » ; c) servir à un programme « dont l'expression est fondamentalement similaire » ou à tout autre acte portant atteinte au droit d'auteur | Fonde et borne la publication |
| Art. 6 §3 | Pas d'application qui cause « un préjudice injustifié aux intérêts légitimes du titulaire » ou porte atteinte à l'exploitation normale du programme | Limite générale |
| Art. 8 | Les autres régimes (brevets, marques, concurrence déloyale, secret des affaires, droit des contrats) restent applicables. « Toute disposition contractuelle contraire à l'article 6 ou aux exceptions prévues à l'article 5, paragraphes 2 et 3, est nulle et non avenue » | Rend inopposable l'interdiction de la licence pour ces actes |
| Considérant 10 | L'interopérabilité est « la capacité d'échanger des informations et d'utiliser mutuellement les informations échangées » ; les parties d'un programme qui assurent l'interconnexion sont ses « interfaces » | Un exporter qui échange des commandes avec le firmware de la carte répond à cette définition |
| Considérant 15 | L'exception vise notamment à « permettre l'interconnexion de tous les éléments d'un système informatique, y compris ceux de fabricants différents » | Idem |

### Jurisprudence de la Cour de justice

- **CJUE, grande chambre, 2 mai 2012, SAS Institute, C-406/10**, ECLI:EU:C:2012:259 (lu sur <https://eur-lex.europa.eu/legal-content/FR/TXT/?uri=CELEX:62010CJ0406>). Ni la fonctionnalité d'un programme, ni son langage de programmation, ni « le format de fichiers de données » ne sont une forme d'expression protégée. Le licencié peut observer, étudier ou tester le fonctionnement du programme pendant les opérations couvertes par sa licence. La Cour rappelle que toute clause contraire à ces exceptions est nulle (directive 91/250, art. 9 §1, devenu l'art. 8 de la directive 2009/24).
- **CJUE, 6 octobre 2021, Top System, C-13/20**, ECLI:EU:C:2021:811 (lu sur <https://eur-lex.europa.eu/legal-content/FR/TXT/?uri=CELEX:62020CJ0013>). L'acquéreur légitime peut décompiler un programme pour en corriger les erreurs, sans remplir les conditions de l'article 6, mais « dans le respect, le cas échéant, des conditions prévues contractuellement ». Cet arrêt ne fonde pas le projet. Il montre que la décompilation n'est pas interdite par principe, et que l'article 5 §1, contrairement à l'article 6 et à l'article 5 §2 et §3, peut être aménagé par contrat.

### Directive (UE) 2016/943 (secret des affaires)

Lue sur <https://eur-lex.europa.eu/legal-content/FR/TXT/?uri=CELEX:32016L0943>.

- **Art. 3 §1 b)** : l'obtention est licite par « l'observation, l'étude, le démontage ou le test d'un produit » détenu de façon licite par une personne « qui n'est pas liée par une obligation juridiquement valide de limiter l'obtention du secret d'affaires ».
- **Art. 3 §2** : l'obtention, l'utilisation ou la divulgation est licite « dans la mesure où elle est requise ou autorisée par le droit de l'Union ou le droit national ».

La licence d'ARCCONF déclare que l'outil contient des secrets d'affaires et interdit la rétro-ingénierie. Pour les actes que la directive 2009/24 autorise malgré le contrat (art. 8), cette clause n'est pas une obligation « juridiquement valide », et l'article 3 §2 rend ces actes licites. C'est un argument, à apprécier acte par acte : aucun arrêt directement applicable n'a été identifié. La qualification même de secret d'affaires suppose, pour chaque information, qu'elle soit secrète, qu'elle ait une valeur commerciale du fait de ce secret et qu'elle fasse l'objet de mesures de protection raisonnables (art. 2) ; l'obtention, l'utilisation et la divulgation s'analysent séparément (art. 3 et 4).

### Loi applicable

La licence d'ARCCONF (§12) désigne le droit californien, sauf pour le droit d'auteur, soumis au droit fédéral américain. Deux règlements européens limitent la portée de ce choix pour un utilisateur établi dans l'Union :

- **Règlement (CE) 864/2007 (Rome II), art. 8** (lu sur <https://eur-lex.europa.eu/legal-content/FR/TXT/?uri=CELEX:32007R0864>) : la loi applicable à une atteinte à un droit de propriété intellectuelle est « celle du pays pour lequel la protection est revendiquée ». « Il ne peut être dérogé à la loi applicable en vertu du présent article par un accord ». La loi applicable dépend donc du pays pour lequel une protection serait revendiquée : pour une revendication portant sur la France, c'est le droit français, exceptions comprises. Une diffusion en ligne peut faire revendiquer une protection pour plusieurs pays.
- **Règlement (CE) 593/2008 (Rome I)** (lu sur <https://eur-lex.europa.eu/legal-content/FR/TXT/?uri=CELEX:32008R0593>) : pour un contrat conclu par un consommateur, le choix de loi « ne peut cependant avoir pour résultat de priver le consommateur de la protection » des dispositions impératives de sa résidence habituelle (art. 6 §2), lorsque le professionnel dirige son activité vers ce pays (art. 6 §1). Les lois de police du juge saisi restent applicables (art. 9 §2). Qu'une installation sur un équipement personnel relève d'un contrat de consommation, et que l'article L122-6-1 du CPI soit une loi de police, n'est pas établi [Supposition].

## 3. France

### Code de la propriété intellectuelle, article L122-6-1

Version en vigueur depuis le 26 novembre 2021 : <https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000044365559>.

- **III** : la personne ayant le droit d'utiliser le logiciel peut, sans autorisation, « observer, étudier ou tester le fonctionnement ou la sécurité de ce logiciel ».
- **IV** : transposition de l'article 6 de la directive. Les informations obtenues ne peuvent être « ni communiquées à des tiers sauf si cela est nécessaire à l'interopérabilité du logiciel créé de façon indépendante ».
- **Dernier alinéa** : « Toute stipulation contraire aux dispositions prévues aux II, III et IV du présent article est nulle et non avenue. »

### Code de commerce, articles L151-3 et L151-7 (secret des affaires)

- **L151-3** (<https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000037266559>) : l'observation, l'étude, le démontage ou le test d'un produit détenu de façon licite est un mode d'obtention licite d'un secret des affaires, « sauf stipulation contractuelle interdisant ou limitant l'obtention du secret ».
- **L151-7** (<https://www.legifrance.gouv.fr/codes/article_lc/LEGIARTI000037266571>) : « Le secret des affaires n'est pas opposable lorsque l'obtention, l'utilisation ou la divulgation du secret est requise ou autorisée par le droit de l'Union européenne […] ou le droit national ».

Pris seul, L151-3 donne effet à la clause de la licence. Mais l'obtention et la communication limitées à l'interopérabilité sont autorisées par l'article L122-6-1 du CPI, qui annule la clause contraire : L151-7 rendrait alors le secret inopposable à ces actes [Probable]. Cet argument est conditionnel : il suppose que l'information soit un secret des affaires au sens de L151-1, et il s'apprécie séparément pour l'obtention, l'utilisation et la divulgation (L151-4 à L151-6). Hors de ce périmètre (méthode, éléments internes de l'outil), la clause garde son effet : c'est une raison de plus pour ne pas les publier.

## 4. États-Unis

La licence d'ARCCONF désigne le droit californien et le droit fédéral américain. Ces textes comptent pour une action intentée aux États-Unis, et parce que le dépôt est hébergé par un service américain.

| Texte | Contenu | Source lue |
|---|---|---|
| 17 U.S.C. § 102(b) | Le droit d'auteur ne protège jamais une idée, une procédure, un procédé, un système ou une méthode de fonctionnement | <https://www.law.cornell.edu/uscode/text/17/102> |
| 17 U.S.C. § 1201(f) | Exception d'interopérabilité à l'interdiction de contourner une mesure technique de protection ; le (3) permet de communiquer les informations obtenues « solely for the purpose of enabling interoperability » | <https://www.law.cornell.edu/uscode/text/17/1201> |
| 18 U.S.C. § 1839(6)(B) (Defend Trade Secrets Act) | Les moyens illicites d'obtention d'un secret d'affaires « do[] not include reverse engineering, independent derivation, or any other lawful means of acquisition » | <https://www.law.cornell.edu/uscode/text/18/1839> |
| Cal. Civ. Code § 3426.1(a) | « Reverse engineering or independent derivation alone shall not be considered improper means. » | <https://codes.findlaw.com/ca/civil-code/civ-sect-3426-1/> (le site officiel refuse les accès automatisés) |

Le § 1201(f) est une exception au seul régime anticontournement, sous conditions (copie obtenue licitement, finalité d'interopérabilité, information non disponible autrement, respect des autres droits). Aucune mesure technique de protection n'a été contournée : l'outil a été analysé tel qu'il est distribué.

### Jurisprudence

- **Sega Enterprises v. Accolade, 977 F.2d 1510 (9th Cir. 1992)** : le désassemblage d'un programme pour accéder à ses éléments fonctionnels non protégés, faute d'autre moyen, relève du *fair use*. Résumé du Copyright Office : <https://www.copyright.gov/fair-use/summaries/segaenters-accolade-9thcir1992.pdf>.
- **Sony Computer Entertainment v. Connectix, 203 F.3d 596 (9th Cir. 2000)** : les copies intermédiaires faites pendant la rétro-ingénierie, pour réaliser un produit non contrefaisant et compatible, relèvent du *fair use*. Résumé : <https://www.copyright.gov/fair-use/summaries/sony-connectix-9thcir2000.pdf>.
- **Bowers v. Baystate Technologies, 320 F.3d 1317 (Fed. Cir. 2003)** (<https://caselaw.findlaw.com/court/us-federal-circuit/1202776.html>) : en appliquant le droit du First Circuit, la cour juge que le droit d'auteur fédéral ne fait pas obstacle à une clause de licence qui interdit la rétro-ingénierie ; sa violation engage la responsabilité contractuelle. L'opinion dissidente s'appuie sur *Vault Corp. v. Quaid Software*, 847 F.2d 255 (5th Cir. 1988), qui avait jugé préemptée une loi d'État (Louisiane) validant de telles clauses ; la portée de cette décision pour une clause purement contractuelle reste discutée.
- **Aqua Connect, Inc. v. Code Rebel, LLC (C.D. Cal. 2012)** : une rétro-ingénierie contraire à une licence ne constitue pas, à elle seule, un moyen illicite au sens du droit californien du secret d'affaires ; elle peut fonder une action contractuelle. Décision de première instance, connue ici par l'analyse de Littler du 27 février 2012 : <https://www.littler.com/news-analysis/asap/california-court-finds-obtaining-trade-secrets-through-reverse-engineering>.

En droit américain, *Sega* et *Connectix* soutiennent une défense de *fair use* pour les copies intermédiaires, sur leurs faits. Le contenu publié, le contrat et le secret d'affaires s'analysent séparément ; le risque le plus net est contractuel, car l'interdiction de la licence n'y est pas nulle comme dans l'Union européenne.

## 5. Autres juridictions

| Pays | Texte | Contenu | Source lue |
|---|---|---|---|
| Royaume-Uni | Copyright, Designs and Patents Act 1988, s.50B | Décompilation permise pour créer un programme indépendant interopérable ; les informations ne doivent pas être communiquées à quelqu'un qui n'en a pas besoin pour cet objectif ; une clause contraire est sans effet (4) | <https://www.legislation.gov.uk/ukpga/1988/48/section/50B> |
| Royaume-Uni | CDPA 1988, s.296A | Est nulle toute clause qui interdit ou restreint la décompilation dans les conditions de s.50B(2), ou l'observation et le test selon s.50BA | <https://www.legislation.gov.uk/ukpga/1988/48/section/296A> |
| Allemagne | UrhG § 69e | Décompilation pour l'interopérabilité ; communication à des tiers seulement si nécessaire à l'interopérabilité | <https://www.gesetze-im-internet.de/urhg/__69e.html> |
| Allemagne | UrhG § 69g (2) | Nullité des clauses contraires à § 69d (2), (3), (5), (7) et à § 69e | <https://www.gesetze-im-internet.de/urhg/__69g.html> |
| Suisse | Loi sur le droit d'auteur (RS 231.1), art. 21 | La personne autorisée à utiliser un logiciel peut obtenir par décryptage du code des informations sur ses interfaces ; elles ne peuvent servir qu'à « développer, entretenir et utiliser des logiciels interopérables » | <https://www.fedlex.admin.ch/eli/cc/1993/1798_1798_1798/fr#art_21> |
| Canada | Loi sur le droit d'auteur (L.R.C. 1985, ch. C-42), art. 30.61 | Le propriétaire ou licencié peut reproduire un programme pour obtenir l'information qui le rend interopérable, si l'utilisation ou la communication de cette information est nécessaire pour rendre les programmes interopérables ou évaluer leur interopérabilité | <https://laws-lois.justice.gc.ca/fra/lois/c-42/page-9.html> |
| Canada | même loi, art. 41.12 | Contournement d'une mesure technique pour l'interopérabilité ; l'information obtenue peut être communiquée à toute autre personne dans ce but (4) | <https://laws-lois.justice.gc.ca/fra/lois/c-42/page-12.html> |
| Australie | Copyright Act 1968, s.47D | Reproduction ou adaptation permise pour obtenir l'information nécessaire à un programme interopérable créé indépendamment, si elle n'est pas facilement disponible par une autre source | <https://www.legislation.gov.au/C1968A00063/latest/text> |
| Australie | même loi, s.47H | Une clause qui exclut ou limite s.47D « has no effect » | idem |

Tous les États membres de l'Union appliquent l'article 6 de la directive 2009/24 dans leur droit national. Le Royaume-Uni, l'Allemagne, le Canada et l'Australie limitent aussi la communication à ce qui est nécessaire à l'interopérabilité. Le texte suisse autorise expressément l'usage des informations pour « entretenir » le logiciel interopérable.

## 6. Application au projet

### Quelle information est « nécessaire à l'interopérabilité » ?

L'état fonctionnel d'un volume ou d'un disque (dégradé, en reconstruction) est déjà accessible par d'autres voies publiques : l'affichage documenté d'ARCCONF, et la MIB SNMP « ADAPTEC-UNIVERSAL-STORAGE-MIB » servie par l'agent de maxView. Ces voies imposent d'exécuter les programmes du constructeur : ARCCONF en root, ou les services de maxView, qui tournent en root et écoutent sur le réseau (constaté sur l'hôte de référence). Elles ne disent rien de l'interface entre un programme et le pilote.

L'exporter est un programme indépendant qui échange directement des commandes avec le firmware de la carte, à travers le pilote. L'information nécessaire à cette interopérabilité est le **format de ces échanges** : quelles requêtes envoyer, comment lire les réponses, comment en déduire un état. Aucune spécification publique de ce format n'a été trouvée (recherche du 23 novembre 2026) : ni dans les pilotes Linux et FreeBSD, ni dans la documentation du constructeur (guides d'ARCCONF, qui ne décrivent que la ligne de commande), ni sous forme de SDK ou d'API documentée, ni par un programme partenaire public.

Ce point est le plus exposé à la contestation : un contradicteur pourrait soutenir que l'information utile est l'état fonctionnel, déjà accessible. C'est pourquoi le projet privilégie l'observation (art. 5 §3, sans cette condition) et ne recourt à la décompilation que pour ce que l'observation ne montre pas.

### Ce qui a exigé la décompilation

| Information [interopérabilité] | Pourquoi l'observation ne suffisait pas |
|---|---|
| Sens des bits d'état d'un membre (reconstruction, défaillance) | Sur l'hôte de référence, tous les membres sont sains : leurs octets d'état valent 0. Les autres valeurs ne peuvent être observées qu'en dégradant un volume qui contient des données. |
| Règle de calcul de l'état d'un volume selon ses membres et son niveau RAID | Même raison : un seul état (optimal) est observable. |
| Conditions des états de disque (en panne, en reconstruction) | Même raison. |
| Critère des cartes qui utilisent ces lectures (section 7 d'[aacraid-interface.md](aacraid-interface.md)) | L'observation ne porte que sur la carte disponible ; elle ne dit pas quelles autres cartes répondent de la même façon. |

Toutes les autres informations de [aacraid-interface.md](aacraid-interface.md) viennent des sources publiques ou de l'observation.

### Ce qui est communiqué, et pourquoi

| Artefact publié | Destinataires | Nécessité pour l'interopérabilité |
|---|---|---|
| Code source de l'exporter | Ses utilisateurs | L'exporter est diffusé sous forme de code source, comme tout logiciel libre. Il envoie des commandes au firmware avec des privilèges élevés : son utilisateur doit pouvoir vérifier la liste blanche des commandes et la reconstruire lui-même. |
| Description des formats ([aacraid-interface.md](aacraid-interface.md)) | Mainteneurs et contributeurs | Nécessaire pour vérifier le décodage, le corriger et le qualifier sur d'autres cartes à partir de leurs captures. Elle se limite aux champs que l'exporter lit et aux règles qu'il applique. |
| Jeux de test | Mainteneurs | Valeurs synthétiques, ou réponses réelles minimales dont les champs identifiants sont mis à zéro. |
| Méthode d'analyse, éléments internes de l'outil | — | Non communiqués : ils ne sont pas nécessaires au fonctionnement ni à la maintenance de l'exporter. |

Une lecture stricte de l'article 6 §2 b) pourrait n'admettre que des communications ciblées, et non une diffusion publique. Le projet retient la diffusion publique parce que ses destinataires (utilisateurs, contributeurs) ne sont pas connus à l'avance ; c'est le risque principal identifié à la section 7. La loi suisse (art. 21) admet expressément l'usage des informations pour « entretenir » un logiciel interopérable ; la loi canadienne (art. 30.61), pour en « évaluer » l'interopérabilité.

### Conditions de l'article 6

| Condition | Situation du projet | Appréciation |
|---|---|---|
| §1 a) Actes faits par une personne ayant le droit d'utiliser une copie | L'analyse a été faite par l'utilisateur de la copie d'ARCCONF fournie par le constructeur pour cette carte et installée sur son propre serveur | Remplie [Probable : suppose une licence acceptée régulièrement par cet utilisateur] |
| §1 b) Informations pas déjà facilement accessibles | Format des échanges avec le firmware : aucune source publique. Sources publiques et observation utilisées d'abord ; décompilation seulement pour ce qu'elles ne montrent pas (tableau ci-dessous) | Remplie pour le format des échanges [Probable] |
| §1 c) Actes limités aux parties nécessaires | Analyse ciblée sur les lectures utilisées par l'exporter | Remplie [Probable] |
| §2 a) Pas d'autre fin que l'interopérabilité | Les informations servent seulement à lire l'état | Remplie |
| §2 b) Communication à des tiers seulement si nécessaire | Voir « Ce qui est communiqué, et pourquoi » | Argumentée artefact par artefact ; c'est la condition la plus discutable |
| §2 c) Pas de programme fondamentalement similaire | L'exporter ne gère pas la carte et ne reprend ni interface ni code de l'outil | Remplie |
| §3 Pas de préjudice injustifié | L'outil constructeur reste nécessaire pour gérer la carte | Remplie [Probable] |

### Clauses de la licence d'ARCCONF

- **§2 (interdiction de la rétro-ingénierie)** : nulle pour les actes couverts par l'art. 5 §3 et l'art. 6 (directive, art. 8 ; CPI, dernier alinéa de L122-6-1 ; CDPA s.296A ; UrhG § 69g ; Copyright Act australien s.47H).
- **§4 (confidentialité)** : ne peut pas empêcher une communication que l'art. 6 §2 b) permet, puisque l'art. 8 annule toute clause contraire à l'article 6 [Probable]. Elle reste applicable au reste : la méthode et les éléments internes ne sont pas publiés.
- **§12 (droit californien)** : voir la section 2 (loi applicable) et la section 7.

## 7. Risques identifiés

1. **Diffusion publique (art. 6 §2 b)).** Une lecture stricte pourrait n'admettre que des communications ciblées. Voir la section 6, « Ce qui est communiqué, et pourquoi ».
2. **Contestation de la condition §1 b).** L'argument repose sur la distinction entre l'état fonctionnel, accessible, et le format des échanges, non publié (section 6).
3. **Action contractuelle aux États-Unis.** Selon *Bowers v. Baystate*, une juridiction américaine pourrait appliquer l'interdiction de la licence. La nullité de la clause dans l'Union ne garantit pas l'issue d'une action intentée aux États-Unis.
4. **Hébergement du dépôt.** Un hébergeur américain peut recevoir une demande de retrait (17 U.S.C. § 512), qui peut rendre le dépôt indisponible le temps de la procédure, quel que soit son bien-fondé. Le dépôt ne contient aucun code de l'outil, ce qui fonde une contre-notification.
5. **Publication future par le constructeur.** Si le constructeur publie une interface documentée, la condition §1 b) tombe pour les analyses futures portant sur les informations qu'elle couvre, et l'exporter doit alors privilégier cette source. Une publication postérieure ne change pas les conditions dans lesquelles l'analyse initiale a été faite.
